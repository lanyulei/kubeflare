package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

type Manager struct {
	timeout  time.Duration
	draining atomic.Bool
	mu       sync.RWMutex
	checkers []Checker
}

func NewManager(timeout time.Duration, checkers ...Checker) *Manager {
	return &Manager{
		timeout:  timeout,
		checkers: checkers,
	}
}

// AddChecker 在运行期追加一个就绪检查项(并发安全),供启动后才就绪的长生命周期
// 组件(如 Agent 的 MCP server)接入 /readyz。nil 检查项忽略。
func (m *Manager) AddChecker(checker Checker) {
	if checker == nil {
		return
	}
	m.mu.Lock()
	m.checkers = append(m.checkers, checker)
	m.mu.Unlock()
}

func (m *Manager) SetDraining(draining bool) {
	m.draining.Store(draining)
}

func (m *Manager) LiveHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func (m *Manager) ReadyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.draining.Load() {
			writeReadiness(w, http.StatusServiceUnavailable, map[string]string{"status": "draining"})
			return
		}

		status := http.StatusOK
		details := map[string]string{"status": "ready"}
		m.mu.RLock()
		checkers := make([]Checker, len(m.checkers))
		copy(checkers, m.checkers)
		m.mu.RUnlock()
		for _, checker := range checkers {
			ctx, cancel := context.WithTimeout(r.Context(), m.timeout)
			err := checker.Check(ctx)
			cancel()
			if err != nil {
				status = http.StatusServiceUnavailable
				details[checker.Name()] = err.Error()
			} else {
				details[checker.Name()] = "ok"
			}
		}

		writeReadiness(w, status, details)
	})
}

func writeReadiness(w http.ResponseWriter, status int, details map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(details)
}

type FuncChecker struct {
	CheckFunc func(context.Context) error
	CheckName string
}

func (f FuncChecker) Name() string {
	return f.CheckName
}

func (f FuncChecker) Check(ctx context.Context) error {
	if f.CheckFunc == nil {
		return errors.New("health check not configured")
	}
	return f.CheckFunc(ctx)
}
