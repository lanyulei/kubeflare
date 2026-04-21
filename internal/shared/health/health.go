package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	checkers []Checker
}

func NewManager(timeout time.Duration, checkers ...Checker) *Manager {
	return &Manager{
		timeout:  timeout,
		checkers: checkers,
	}
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
		for _, checker := range m.checkers {
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
