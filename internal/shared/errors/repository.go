package errors

import (
	stdErrors "errors"
	"net/http"
	"strings"

	"gorm.io/gorm"
)

// RepositoryErrorOptions 描述把仓储层错误映射为 AppError 时的模块定制项。
// NotFound 段必填(Code 留 0 时回退 CodeNotFound);Conflict 段仅在
// ConflictMessage 非空时启用(Code 留 0 时回退 CodeConflict)。
type RepositoryErrorOptions struct {
	NotFoundCode    int
	NotFoundMessage string
	ConflictCode    int
	ConflictMessage string
}

// MapRepository 把仓储层错误统一映射为 AppError:记录不存在 → 404,
// 唯一键冲突 → 409,其余原样透传。各模块以薄封装传入自身的 Code/文案复用此函数,
// 避免在多个 service 各写一份 NotFound/Conflict 判定(此前 cluster/ai 的判定已开始分叉)。
func MapRepository(err error, opts RepositoryErrorOptions) error {
	if err == nil {
		return nil
	}
	if stdErrors.Is(err, gorm.ErrRecordNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
		code := opts.NotFoundCode
		if code == 0 {
			code = CodeNotFound
		}
		return &AppError{
			Code:    code,
			Message: opts.NotFoundMessage,
			Status:  http.StatusNotFound,
			Err:     err,
		}
	}
	if opts.ConflictMessage != "" && stdErrors.Is(err, gorm.ErrDuplicatedKey) {
		code := opts.ConflictCode
		if code == 0 {
			code = CodeConflict
		}
		return &AppError{
			Code:    code,
			Message: opts.ConflictMessage,
			Status:  http.StatusConflict,
			Err:     err,
		}
	}
	return err
}
