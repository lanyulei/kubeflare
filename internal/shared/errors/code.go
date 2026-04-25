package errors

const (
	CodeSuccess = 20000

	CodeInternal     = 30000
	CodeBadRequest   = 30001
	CodeValidation   = 30001
	CodeUnauthorized = 30002
	CodeForbidden    = 30003
	CodeNotFound     = 30004
	CodeConflict     = 30005
	CodeTimeout      = 30006

	CodeUserNotFound      = 40101
	CodeUserAlreadyExists = 40102

	CodeClusterNotFound         = 40301
	CodeClusterAlreadyExists    = 40302
	CodeClusterRequired         = 40303
	CodeInvalidProxyPath        = 40304
	CodeInvalidClusterTransport = 40305
	CodeUpstreamUnavailable     = 40306
)
