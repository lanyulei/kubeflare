package errors

import (
	"context"
	stdErrors "errors"
	"net/http"

	"github.com/go-playground/validator/v10"
)

func From(err error) *AppError {
	if err == nil {
		return nil
	}

	var appErr *AppError
	if stdErrors.As(err, &appErr) {
		return appErr
	}

	var validationErrs validator.ValidationErrors
	if stdErrors.As(err, &validationErrs) {
		return &AppError{
			Code:    CodeValidation,
			Message: validationErrs.Error(),
			Status:  http.StatusBadRequest,
			Err:     err,
		}
	}

	if stdErrors.Is(err, context.DeadlineExceeded) {
		return &AppError{
			Code:    CodeTimeout,
			Message: "request timed out",
			Status:  http.StatusGatewayTimeout,
			Err:     err,
		}
	}

	return &AppError{
		Code:    CodeInternal,
		Message: "internal server error",
		Status:  http.StatusInternalServerError,
		Err:     err,
	}
}
