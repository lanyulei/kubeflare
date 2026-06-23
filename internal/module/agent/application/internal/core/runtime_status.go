package core

import (
	"context"
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

func (s *Service) GetRuntimeStatus(ctx context.Context, userID string, clusterID string) (RuntimeStatus, error) {
	if _, err := normalizeUserID(userID); err != nil {
		return RuntimeStatus{}, err
	}
	if s == nil {
		return RuntimeStatus{}, nil
	}
	s.refreshRuntimeConfig(ctx, false)

	tools := s.toolRegistry.List()
	skills := s.skillRegistry.List()
	toolStatus := buildRuntimeToolStatus(tools)
	prometheus := s.prometheusStatus
	prometheus.ToolCount = toolStatus.Prometheus
	prometheus.Enabled = prometheus.Enabled || (toolStatus.Prometheus > 0 && strings.TrimSpace(prometheus.Service) != "")
	if prometheus.Enabled && s.prometheusHealthProvider != nil {
		health := s.prometheusHealthProvider(ctx, strings.TrimSpace(clusterID))
		prometheus.Healthy = health.Healthy
		prometheus.LastError = health.LastError
		prometheus.LatencyMS = health.LatencyMS
		if !health.LastCheckedAt.IsZero() {
			checkedAt := health.LastCheckedAt
			prometheus.LastCheckedAt = &checkedAt
		}
	}

	return RuntimeStatus{
		Features:       s.runtimeFeatureStatus(),
		Loop:           s.runtimeLoopStatus(),
		Concurrency:    s.runtimeConcurrencyStatus(),
		Repositories:   s.runtimeRepositoryStatus(),
		Tools:          toolStatus,
		Skills:         buildRuntimeSkillStatus(skills),
		MCPServers:     s.runtimeMCPServers(),
		Prometheus:     prometheus,
		RuntimeVersion: s.runtimeVersion.Load(),
	}, nil
}

func (s *Service) runtimeFeatureStatus() RuntimeFeatureStatus {
	opts := s.opts
	return RuntimeFeatureStatus{
		LLMRouting:         boolPointerValue(opts.LLMRouting, true),
		StreamThink:        boolPointerValue(opts.StreamThink, true),
		Planning:           boolPointerValue(opts.Planning, true),
		Reflection:         boolPointerValue(opts.Reflection, true),
		HypothesisLedger:   boolPointerValue(opts.HypothesisLedger, true),
		Playbook:           boolPointerValue(opts.Playbook, true),
		ObserveCompression: opts.ObserveCompression,
		CaseLibrary:        s.caseLibraryEnabled(),
		SemanticRetrieval:  s.semanticRetrievalEnabled(),
		Replanning:         s.replanningEnabled(),
		RouteLearning:      s.routeLearningEnabled(),
	}
}

func (s *Service) runtimeLoopStatus() RuntimeLoopStatus {
	opts := s.opts
	return RuntimeLoopStatus{
		MaxSteps:             opts.MaxSteps,
		MaxTokenBudget:       opts.MaxTokenBudget,
		MaxToolErrorsPerStep: opts.MaxToolErrorsPerStep,
		StepTimeoutMS:        opts.StepTimeout.Milliseconds(),
		ToolChoice:           opts.ToolChoice,
		MaxReflectionSteps:   opts.MaxReflectionSteps,
		MaxReflections:       opts.MaxReflections,
		ReflectionJurors:     opts.ReflectionJurors,
		CaseFewShotLimit:     opts.CaseFewShotLimit,
		CaseCacheSize:        opts.CaseCacheSize,
		RouteFewShotLimit:    opts.RouteFewShotLimit,
		RouteCacheSize:       opts.RouteCacheSize,
		ReplanInterval:       opts.ReplanInterval,
		MaxReplans:           opts.MaxReplans,
	}
}

func (s *Service) runtimeConcurrencyStatus() RuntimeConcurrencyStatus {
	return RuntimeConcurrencyStatus{
		MaxConcurrentRunsPerUser: s.opts.MaxConcurrentRunsPerUser,
		MaxConcurrentRuns:        s.opts.MaxConcurrentRuns,
		DistributedSemaphore:     s.semaphore != nil,
		InstanceID:               s.instanceID,
	}
}

func (s *Service) runtimeRepositoryStatus() RuntimeRepositoryStatus {
	return RuntimeRepositoryStatus{
		RuntimeConfig: s.runtimeConfigRepository() != nil,
		RouteFeedback: s.feedbackRepo != nil,
		DiagnosisCase: s.caseRepo != nil,
		RunMetrics:    s.metricsRepo != nil,
		RunFeedback:   s.runFeedbackRepo != nil,
		Embedding:     s.embeddingGen != nil && s.embeddingGen.Available(),
	}
}

func (s *Service) runtimeMCPServers() []RuntimeMCPServerStatus {
	if s.mcpStatusProvider == nil {
		return []RuntimeMCPServerStatus{}
	}
	items := s.mcpStatusProvider()
	if items == nil {
		return []RuntimeMCPServerStatus{}
	}
	return items
}

func buildRuntimeToolStatus(tools []domain.ToolDefinition) RuntimeToolStatus {
	status := RuntimeToolStatus{Total: len(tools)}
	for _, tool := range tools {
		if tool.Enabled {
			status.Enabled++
		} else {
			status.Disabled++
		}
		if tool.Origin == domain.TOOL_ORIGIN_MCP || strings.HasPrefix(tool.Source, domain.TOOL_SOURCE_MCP_PREFIX) {
			status.MCP++
		}
		if tool.Source == domain.TOOL_SOURCE_MONITORING {
			status.Prometheus++
		}
	}
	return status
}

func buildRuntimeSkillStatus(skills []domain.SkillDefinition) RuntimeSkillStatus {
	status := RuntimeSkillStatus{Total: len(skills)}
	for _, skill := range skills {
		if skill.Enabled {
			status.Enabled++
		} else {
			status.Disabled++
		}
	}
	return status
}

func boolPointerValue(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}
