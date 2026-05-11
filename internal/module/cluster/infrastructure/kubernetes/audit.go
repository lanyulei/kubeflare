package kubernetes

import (
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

// sessionLimiter caps the number of simultaneous upgrade sessions a single
// authenticated subject may hold open at once. It is a pure in-memory map;
// process restart resets the counter, which is acceptable because every open
// session also dies when the process dies.
type sessionLimiter struct {
	max     int
	counts  sync.Map // map[string]*int64
}

func newSessionLimiter(max int) *sessionLimiter {
	if max < 0 {
		max = 0
	}
	return &sessionLimiter{max: max}
}

// Acquire reserves a session slot for the subject. If the cap is exceeded
// the returned release func is nil and ok is false. Callers MUST defer
// release() exactly once when ok is true.
func (l *sessionLimiter) Acquire(subject string) (release func(), ok bool) {
	if l == nil || l.max <= 0 || subject == "" {
		return func() {}, true
	}
	value, _ := l.counts.LoadOrStore(subject, new(int64))
	counter := value.(*int64)
	if atomic.AddInt64(counter, 1) > int64(l.max) {
		atomic.AddInt64(counter, -1)
		return nil, false
	}
	return func() {
		atomic.AddInt64(counter, -1)
	}, true
}

// parseExecTarget extracts namespace / pod / container from an upstream
// path like /api/v1/namespaces/<ns>/pods/<pod>/exec. Returns empty strings
// for paths that do not match.
func parseExecTarget(upstreamPath string, query url.Values) (namespace, pod, container string, isExec bool) {
	segments := strings.Split(strings.TrimPrefix(upstreamPath, "/"), "/")
	for i := 0; i < len(segments)-1; i++ {
		if segments[i] == "namespaces" {
			namespace = segments[i+1]
		}
		if segments[i] == "pods" && i+1 < len(segments) {
			pod = segments[i+1]
		}
	}
	last := ""
	if len(segments) > 0 {
		last = segments[len(segments)-1]
	}
	switch last {
	case "exec", "attach", "portforward":
		isExec = true
	}
	container = query.Get("container")
	return
}
