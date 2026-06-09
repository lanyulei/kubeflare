package errors

import (
	"errors"
	"net/http"
	"testing"

	"gorm.io/gorm"
)

// TestMapRepositoryNotFound 验证记录不存在映射为 404 + 指定 Code/文案。
func TestMapRepositoryNotFound(t *testing.T) {
	opts := RepositoryErrorOptions{NotFoundCode: CodeUserNotFound, NotFoundMessage: "user not found"}
	got := MapRepository(gorm.ErrRecordNotFound, opts)
	appErr, ok := got.(*AppError)
	if !ok {
		t.Fatalf("MapRepository returned %T, want *AppError", got)
	}
	if appErr.Status != http.StatusNotFound || appErr.Code != CodeUserNotFound || appErr.Message != "user not found" {
		t.Errorf("notfound mapping = %+v", appErr)
	}

	// 字符串包含 "not found" 也应命中。
	if _, ok := MapRepository(errors.New("record Not Found"), opts).(*AppError); !ok {
		t.Errorf("string 'not found' should map to AppError")
	}
}

// TestMapRepositoryConflict 验证唯一键冲突在启用时映射为 409,未启用时透传。
func TestMapRepositoryConflict(t *testing.T) {
	withConflict := RepositoryErrorOptions{NotFoundMessage: "nf", ConflictCode: CodeConflict, ConflictMessage: "dup"}
	got := MapRepository(gorm.ErrDuplicatedKey, withConflict)
	appErr, ok := got.(*AppError)
	if !ok || appErr.Status != http.StatusConflict || appErr.Code != CodeConflict || appErr.Message != "dup" {
		t.Fatalf("conflict mapping = %v (%T)", got, got)
	}

	// 未配置 ConflictMessage 时,冲突错误原样透传。
	noConflict := RepositoryErrorOptions{NotFoundMessage: "nf"}
	if got := MapRepository(gorm.ErrDuplicatedKey, noConflict); got != gorm.ErrDuplicatedKey {
		t.Errorf("without conflict opt, want passthrough, got %v", got)
	}
}

// TestMapRepositoryDefaultsAndPassthrough 验证 Code 缺省回退与无关错误透传/nil。
func TestMapRepositoryDefaultsAndPassthrough(t *testing.T) {
	if got := MapRepository(nil, RepositoryErrorOptions{}); got != nil {
		t.Errorf("MapRepository(nil) = %v, want nil", got)
	}

	// NotFoundCode 留 0 应回退 CodeNotFound。
	got := MapRepository(gorm.ErrRecordNotFound, RepositoryErrorOptions{NotFoundMessage: "x"})
	if appErr, ok := got.(*AppError); !ok || appErr.Code != CodeNotFound {
		t.Errorf("default NotFoundCode = %v, want CodeNotFound", got)
	}

	other := errors.New("connection refused")
	if got := MapRepository(other, RepositoryErrorOptions{NotFoundMessage: "x"}); got != other {
		t.Errorf("unrelated error should pass through, got %v", got)
	}
}
