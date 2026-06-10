package kubernetes

import (
	"net/url"
	"strings"

	"github.com/lanyulei/kubeflare/internal/shared/limiter"
)

// sessionLimiter caps the number of simultaneous upgrade sessions a single
// authenticated subject may hold open at once. It is a pure in-memory counter;
// process restart resets it, which is acceptable because every open session
// also dies when the process dies.
//
// It is a thin wrapper over shared/limiter's keyed semaphore, which deletes a
// subject's counter once it returns to zero — important here because subjects
// come from external (authenticated) input and would otherwise accumulate
// unboundedly over the process lifetime.
type sessionLimiter struct {
	sem *limiter.KeyedSemaphore
}

func newSessionLimiter(max int) *sessionLimiter {
	return &sessionLimiter{sem: limiter.NewKeyedSemaphore(max)}
}

// Acquire reserves a session slot for the subject. If the cap is exceeded
// the returned release func is nil and ok is false. Callers MUST defer
// release() exactly once when ok is true.
func (l *sessionLimiter) Acquire(subject string) (release func(), ok bool) {
	if l == nil {
		return func() {}, true
	}
	return l.sem.Acquire(subject)
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
