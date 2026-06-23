package run

const (
	// DEFAULT_EVALUATION_WINDOW_DAYS 是评估看板的默认统计窗口(天)。
	DEFAULT_EVALUATION_WINDOW_DAYS = 30
	// MAX_EVALUATION_WINDOW_DAYS 是评估窗口上限,防御异常入参(过大窗口会扫描
	// 全表)。
	MAX_EVALUATION_WINDOW_DAYS = 365
)

type RunMetricsEvaluationRequest struct {
	Days      int
	AgentType string
	ClusterID string
}
