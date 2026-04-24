package errors

import "strconv"

type AppError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
	Err     error  `json:"-"`
}

func (e *AppError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return strconv.Itoa(e.Code)
}

func (e *AppError) Unwrap() error {
	return e.Err
}
