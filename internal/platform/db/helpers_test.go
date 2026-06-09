package db

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

// TestJSONBValue 验证 Value:空值落 "{}",合法 JSON 透出字符串,非法 JSON 报错。
func TestJSONBValue(t *testing.T) {
	got, err := JSONB(nil).Value()
	if err != nil || got != "{}" {
		t.Fatalf("empty JSONB.Value = (%v, %v), want (\"{}\", nil)", got, err)
	}

	got, err = JSONB(`{"a":1}`).Value()
	if err != nil || got != `{"a":1}` {
		t.Fatalf("valid JSONB.Value = (%v, %v), want ({\"a\":1}, nil)", got, err)
	}

	if _, err := JSONB(`{bad`).Value(); err == nil {
		t.Fatalf("invalid JSONB.Value err = nil, want error")
	}
}

// TestJSONBScan 验证 Scan 容忍 nil/[]byte/string,并拒绝不支持的类型。
func TestJSONBScan(t *testing.T) {
	var v JSONB
	if err := v.Scan(nil); err != nil || string(v) != "{}" {
		t.Fatalf("Scan(nil) = (%q, %v), want (\"{}\", nil)", v, err)
	}
	if err := v.Scan([]byte(`{"a":1}`)); err != nil || string(v) != `{"a":1}` {
		t.Fatalf("Scan([]byte) = (%q, %v)", v, err)
	}
	if err := v.Scan(`{"b":2}`); err != nil || string(v) != `{"b":2}` {
		t.Fatalf("Scan(string) = (%q, %v)", v, err)
	}
	if err := v.Scan(123); err == nil {
		t.Fatalf("Scan(int) err = nil, want error")
	}
}

// TestNewJSONB 验证空/非法输入归一为 "{}",合法输入做拷贝。
func TestNewJSONB(t *testing.T) {
	if got := NewJSONB(nil); string(got) != "{}" {
		t.Errorf("NewJSONB(nil) = %q, want {}", got)
	}
	if got := NewJSONB([]byte(`{bad`)); string(got) != "{}" {
		t.Errorf("NewJSONB(invalid) = %q, want {}", got)
	}
	src := []byte(`{"a":1}`)
	got := NewJSONB(src)
	if string(got) != `{"a":1}` {
		t.Errorf("NewJSONB(valid) = %q", got)
	}
	src[0] = 'X' // 修改源不应影响已构造的 JSONB(确认是拷贝)
	if string(got) != `{"a":1}` {
		t.Errorf("NewJSONB should copy input, got %q after mutating source", got)
	}
}

// TestDeleteResult 验证错误透传 / 0 行视为不存在 / 正常返回 nil。
func TestDeleteResult(t *testing.T) {
	sentinel := errors.New("boom")
	if got := DeleteResult(sentinel, 0); got != sentinel {
		t.Errorf("DeleteResult(err,_) = %v, want passthrough", got)
	}
	if got := DeleteResult(nil, 0); got != gorm.ErrRecordNotFound {
		t.Errorf("DeleteResult(nil,0) = %v, want ErrRecordNotFound", got)
	}
	if got := DeleteResult(nil, 1); got != nil {
		t.Errorf("DeleteResult(nil,1) = %v, want nil", got)
	}
}

// TestDeletedAtPtr 验证 Valid/非 Valid 的指针转换。
func TestDeletedAtPtr(t *testing.T) {
	if got := DeletedAtPtr(gorm.DeletedAt{}); got != nil {
		t.Errorf("DeletedAtPtr(invalid) = %v, want nil", got)
	}
	now := time.Now()
	got := DeletedAtPtr(gorm.DeletedAt{Time: now, Valid: true})
	if got == nil || !got.Equal(now) {
		t.Errorf("DeletedAtPtr(valid) = %v, want %v", got, now)
	}
}
