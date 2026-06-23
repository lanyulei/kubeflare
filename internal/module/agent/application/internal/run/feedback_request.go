package run

// SubmitRunFeedbackRequest 是 POST /agent/run/:runID/feedback 的请求体:用户对一次
// 诊断结论的质量反馈。Useful 用指针区分"未提交"与"显式 false",必填。
type SubmitRunFeedbackRequest struct {
	Useful  *bool  `json:"useful" validate:"required"`
	Comment string `json:"comment" validate:"omitempty,max=1024"`
}
