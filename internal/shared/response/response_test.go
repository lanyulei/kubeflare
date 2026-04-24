package response

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

func TestOKWritesIntegerSuccessCode(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	c.Set("request_id", "req-1")

	OK(c, http.StatusOK, gin.H{"name": "kubeflare"})

	var body Envelope
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != sharedErrors.CodeSuccess {
		t.Fatalf("code = %d, want %d", body.Code, sharedErrors.CodeSuccess)
	}
	if body.Message != "成功" {
		t.Fatalf("message = %q, want %q", body.Message, "成功")
	}
	if body.RequestID != "req-1" {
		t.Fatalf("request id = %q, want %q", body.RequestID, "req-1")
	}
}

func TestErrorWritesMappedIntegerCode(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	c.Set("request_id", "req-2")

	Error(c, &sharedErrors.AppError{
		Code:    sharedErrors.CodeUserNotFound,
		Message: "user not found",
		Status:  http.StatusNotFound,
		Err:     errors.New("missing"),
	})

	var body Envelope
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
	if body.Code != sharedErrors.CodeUserNotFound {
		t.Fatalf("code = %d, want %d", body.Code, sharedErrors.CodeUserNotFound)
	}
	if body.Message != "user not found" {
		t.Fatalf("message = %q, want %q", body.Message, "user not found")
	}
}

func TestHTTPStatusErrorWritesIntegerCode(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()

	HTTPStatusError(rr, http.StatusBadRequest, sharedErrors.CodeClusterRequired, "cluster id is required", "req-3")

	var body Envelope
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Code != sharedErrors.CodeClusterRequired {
		t.Fatalf("code = %d, want %d", body.Code, sharedErrors.CodeClusterRequired)
	}
}
