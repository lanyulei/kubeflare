package errors

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"gorm.io/gorm"
)

func TestFromMapsCommonErrorsToIntegerCodes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name       string
		err        error
		wantCode   int
		wantStatus int
	}{
		{name: "timeout", err: context.DeadlineExceeded, wantCode: CodeTimeout, wantStatus: http.StatusGatewayTimeout},
		{name: "not found", err: gorm.ErrRecordNotFound, wantCode: CodeNotFound, wantStatus: http.StatusNotFound},
		{name: "conflict", err: gorm.ErrDuplicatedKey, wantCode: CodeConflict, wantStatus: http.StatusConflict},
		{name: "internal", err: errors.New("boom"), wantCode: CodeInternal, wantStatus: http.StatusInternalServerError},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			appErr := From(tc.err)

			if appErr.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d", appErr.Code, tc.wantCode)
			}
			if appErr.Status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", appErr.Status, tc.wantStatus)
			}
		})
	}
}
