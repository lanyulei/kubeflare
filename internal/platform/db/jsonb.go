package db

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// JSONB 是用于 GORM 的 jsonb 列读写类型:落库时校验为合法 JSON(空值落 "{}"),
// 读取时容忍 nil/[]byte/string 三种底层形态。各模块的 jsonb 字段统一复用此类型,
// 避免在多个 repository 中各写一份相同的 Value/Scan 实现。
type JSONB []byte

// NewJSONB 由原始字节构造 JSONB,空或非法 JSON 归一为 "{}"(与列默认值一致),
// 合法输入做拷贝,避免与调用方共享底层数组。
func NewJSONB(data []byte) JSONB {
	if len(data) == 0 || !json.Valid(data) {
		return JSONB("{}")
	}
	return append(JSONB(nil), data...)
}

// Value 实现 driver.Valuer:空值落 "{}",非法 JSON 拒绝写入。
func (v JSONB) Value() (driver.Value, error) {
	if len(v) == 0 {
		return "{}", nil
	}
	if !json.Valid(v) {
		return nil, fmt.Errorf("invalid jsonb value")
	}
	return string(v), nil
}

// Scan 实现 sql.Scanner:容忍 nil/[]byte/string,nil 归一为 "{}"。
func (v *JSONB) Scan(value any) error {
	switch data := value.(type) {
	case nil:
		*v = JSONB("{}")
	case []byte:
		*v = append((*v)[:0], data...)
	case string:
		*v = append((*v)[:0], data...)
	default:
		return fmt.Errorf("unsupported jsonb scan value %T", value)
	}
	return nil
}
