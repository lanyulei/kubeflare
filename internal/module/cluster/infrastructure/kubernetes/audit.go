package kubernetes

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/shared/coordination"
	"github.com/lanyulei/kubeflare/internal/shared/idgen"
	"github.com/lanyulei/kubeflare/internal/shared/limiter"
)

const sessionLeaseTTL = 90 * time.Second

// sessionLimiter caps the number of simultaneous upgrade sessions a single
// authenticated subject may hold open at once. When a distributed semaphore is
// injected the cap is global across replicas; otherwise it falls back to a
// process-local counter for single-instance development.
//
// It is a thin wrapper over shared/limiter's keyed semaphore, which deletes a
// subject's counter once it returns to zero — important here because subjects
// come from external (authenticated) input and would otherwise accumulate
// unboundedly over the process lifetime.
type sessionLimiter struct {
	sem         *limiter.KeyedSemaphore
	distributed coordination.Semaphore
	max         int
}

func newSessionLimiter(max int, distributed coordination.Semaphore) *sessionLimiter {
	return &sessionLimiter{
		sem:         limiter.NewKeyedSemaphore(max),
		distributed: distributed,
		max:         max,
	}
}

// Acquire reserves a session slot for the subject. If the cap is exceeded
// the returned release func is nil and ok is false. Callers MUST defer
// release() exactly once when ok is true.
func (l *sessionLimiter) Acquire(ctx context.Context, subject string) (release func(), ok bool, err error) {
	if l == nil {
		return func() {}, true, nil
	}
	if l.distributed != nil {
		lease, ok, err := l.distributed.Acquire(ctx, idgen.NewID("kapi-session"), sessionLeaseTTL, coordination.SemaphoreLimit{
			Key:   "kapi:upgrade:user:" + subject,
			Limit: l.max,
		})
		if err != nil || !ok {
			return nil, ok, err
		}
		leaseCtx, stopLease := context.WithCancel(context.WithoutCancel(ctx))
		go refreshSessionLease(leaseCtx, lease)
		return func() {
			stopLease()
			releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			_ = lease.Release(releaseCtx)
		}, true, nil
	}
	release, ok = l.sem.Acquire(subject)
	return release, ok, nil
}

func refreshSessionLease(ctx context.Context, lease coordination.Lease) {
	if lease == nil {
		return
	}
	ticker := time.NewTicker(sessionLeaseTTL / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			alive, err := lease.Refresh(refreshCtx)
			cancel()
			if err != nil || !alive {
				return
			}
		}
	}
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
