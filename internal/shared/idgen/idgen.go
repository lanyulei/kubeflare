package idgen

import (
	"crypto/rand"
	"encoding/hex"
)

// NewID 生成带业务前缀的随机 ID,格式为 "<prefix>-<24 hex>"。
// 用于 AI 会话、消息、Agent 运行、工具调用、证据等实体的主键。
func NewID(prefix string) string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return prefix + "-" + hex.EncodeToString(buf[:])
}
