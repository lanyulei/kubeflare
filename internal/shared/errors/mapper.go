package errors

import (
	"context"
	"encoding/json"
	stdErrors "errors"
	"io"
	"net/http"

	"github.com/go-playground/validator/v10"
	"gorm.io/gorm"
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

	var syntaxErr *json.SyntaxError
	if stdErrors.As(err, &syntaxErr) {
		return &AppError{
			Code:    CodeBadRequest,
			Message: "invalid request body",
			Status:  http.StatusBadRequest,
			Err:     err,
		}
	}

	var typeErr *json.UnmarshalTypeError
	if stdErrors.As(err, &typeErr) {
		return &AppError{
			Code:    CodeBadRequest,
			Message: "invalid request body",
			Status:  http.StatusBadRequest,
			Err:     err,
		}
	}

	if stdErrors.Is(err, io.EOF) {
		return &AppError{
			Code:    CodeBadRequest,
			Message: "request body is required",
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
	if stdErrors.Is(err, gorm.ErrRecordNotFound) {
		return &AppError{
			Code:    CodeNotFound,
			Message: "resource not found",
			Status:  http.StatusNotFound,
			Err:     err,
		}
	}
	if stdErrors.Is(err, gorm.ErrDuplicatedKey) {
		return &AppError{
			Code:    CodeConflict,
			Message: "resource already exists",
			Status:  http.StatusConflict,
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
