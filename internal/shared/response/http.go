package response

import (
	"encoding/json"
	"net/http"

	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

func HTTPError(w http.ResponseWriter, requestID string, err error) {
	appErr := sharedErrors.From(err)
	WriteHTTPEnvelope(w, appErr.Status, Envelope{
		Code:      appErr.Code,
		Message:   appErr.Message,
		RequestID: requestID,
	})
}

func HTTPStatusError(w http.ResponseWriter, status int, code int, message, requestID string) {
	WriteHTTPEnvelope(w, status, Envelope{
		Code:      code,
		Message:   message,
		RequestID: requestID,
	})
}

func WriteHTTPEnvelope(w http.ResponseWriter, status int, envelope Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope)
}
