package application

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	sharedcoord "github.com/lanyulei/kubeflare/internal/shared/coordination"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/safego"
)

type runSlot struct {
	lease        sharedcoord.Lease
	localRelease func()
}

func (s *Service) acquireRunSlot(ctx context.Context, userID string, runID string) (runSlot, bool, error) {
	if s == nil {
		return runSlot{}, false, nil
	}
	if s.semaphore != nil {
		lease, ok, err := s.semaphore.Acquire(ctx, runID, RUN_LEASE_TTL,
			sharedcoord.SemaphoreLimit{
				Key:   "agent:run:global",
				Limit: s.opts.MaxConcurrentRuns,
			},
			sharedcoord.SemaphoreLimit{
				Key:   "agent:run:user:" + userID,
				Limit: s.opts.MaxConcurrentRunsPerUser,
			},
		)
		if err != nil {
			return runSlot{}, false, &sharedErrors.AppError{
				Code:    sharedErrors.CodeInternal,
				Message: "Agent 分布式并发控制不可用,请稍后再试",
				Status:  http.StatusServiceUnavailable,
				Err:     err,
			}
		}
		return runSlot{lease: lease}, ok, nil
	}

	release, ok := s.runLimiter.Acquire(userID)
	if !ok {
		return runSlot{}, false, nil
	}
	return runSlot{localRelease: release}, true, nil
}

func (s *Service) releaseRunSlot(ctx context.Context, slot runSlot) {
	if slot.localRelease != nil {
		slot.localRelease()
		return
	}
	if slot.lease == nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if err := slot.lease.Release(releaseCtx); err != nil {
		s.logPersistError("release run lease", err)
	}
}

func (s *Service) startRunHeartbeat(ctx context.Context, persistCtx context.Context, runID string, lease sharedcoord.Lease, cancelRun context.CancelFunc) {
	if s == nil || strings.TrimSpace(runID) == "" {
		return
	}
	heartbeat := func() bool {
		now := time.Now().UTC()
		leaseExpiresAt := now.Add(RUN_LEASE_TTL)
		if lease != nil {
			refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			alive, err := lease.Refresh(refreshCtx)
			cancel()
			if err != nil {
				s.logAgentWarn("refresh run lease", err, "run_id", runID)
				cancelRun()
				return false
			}
			if !alive {
				s.logAgentWarn("run lease lost", fmt.Errorf("run lease is no longer held"), "run_id", runID)
				cancelRun()
				return false
			}
		}
		if s.repo != nil {
			heartbeatCtx, cancel := context.WithTimeout(persistCtx, 2*time.Second)
			err := s.repo.HeartbeatRun(heartbeatCtx, runID, s.instanceID, now, leaseExpiresAt)
			cancel()
			if err != nil {
				s.logPersistError("heartbeat run", err, "run_id", runID)
			}
		}
		return true
	}

	heartbeat()
	safego.Go(s.logger, "agent run heartbeat", func() {
		ticker := time.NewTicker(RUN_LEASE_REFRESH_INTERVAL)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !heartbeat() {
					return
				}
			}
		}
	})
}

func (s *Service) watchRunCancellation(ctx context.Context, runID string, cancelRun context.CancelFunc) {
	if s == nil || cancelRun == nil || strings.TrimSpace(runID) == "" {
		return
	}
	if s.eventBus != nil {
		topic := runCancelTopic(runID)
		stop, err := s.eventBus.Subscribe(ctx, topic, func(payload string) {
			if strings.TrimSpace(payload) == runID {
				cancelRun()
			}
		})
		if err != nil {
			s.logAgentWarn("subscribe run cancel", err, "run_id", runID)
		} else if stop != nil {
			safego.Go(s.logger, "agent run cancel subscription cleanup", func() {
				<-ctx.Done()
				if err := stop(); err != nil {
					s.logAgentWarn("stop run cancel subscription", err, "run_id", runID)
				}
			})
		}
	}

	safego.Go(s.logger, "agent run cancel poll", func() {
		ticker := time.NewTicker(RUN_CANCEL_POLL_INTERVAL)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.runCancelRequested(context.WithoutCancel(ctx), runID) {
					cancelRun()
					return
				}
			}
		}
	})
}

func (s *Service) requestRunCancel(ctx context.Context, runID string) {
	if s == nil || s.eventBus == nil || strings.TrimSpace(runID) == "" {
		return
	}
	if err := s.eventBus.Signal(ctx, runCancelTopic(runID), runID, RUN_CANCEL_SIGNAL_TTL); err != nil {
		s.logAgentWarn("publish run cancel", err, "run_id", runID)
	}
}

func (s *Service) runCancelRequested(ctx context.Context, runID string) bool {
	runID = strings.TrimSpace(runID)
	if s == nil || runID == "" {
		return false
	}
	if s.eventBus != nil {
		signaled, err := s.eventBus.Signaled(ctx, runCancelTopic(runID), runID)
		if err != nil {
			s.logAgentWarn("check run cancel signal", err, "run_id", runID)
		}
		if signaled {
			return true
		}
	}
	if s.repo == nil {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	run, err := s.repo.GetRun(checkCtx, runID)
	if err != nil {
		return false
	}
	return run.Status == domain.RUN_STATUS_CANCELLED
}

func runCancelTopic(runID string) string {
	return RUN_CANCEL_TOPIC_PREFIX + "." + strings.TrimSpace(runID)
}
