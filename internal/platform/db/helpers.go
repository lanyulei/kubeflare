package db

import (
	"time"

	"gorm.io/gorm"
)

// DeleteResult 统一软/硬删除后的结果判定:底层错误优先返回,影响行数为 0 视为
// 记录不存在(返回 gorm.ErrRecordNotFound),由上层错误映射转换为 404。各 repository
// 的删除方法复用此函数,避免重复实现相同判定。
func DeleteResult(err error, rowsAffected int64) error {
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// DeletedAtPtr 把 gorm.DeletedAt 归一为 *time.Time:未删除返回 nil,已删除返回时间指针。
// 各 repository 的 record→domain 映射复用此函数,避免重复的 Valid 判定样板。
func DeletedAtPtr(deletedAt gorm.DeletedAt) *time.Time {
	if deletedAt.Valid {
		t := deletedAt.Time
		return &t
	}
	return nil
}
