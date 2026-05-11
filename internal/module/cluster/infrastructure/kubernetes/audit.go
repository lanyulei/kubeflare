package kubernetes

import (
	"encoding/binary"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
	"unicode/utf8"
)

// auditTapWriter forwards writes to the underlying writer unchanged, while
// also copying the bytes to a bounded channel for asynchronous inspection.
// If the channel is full the chunk is silently dropped so the proxy never
// stalls behind a slow audit goroutine.
type auditTapWriter struct {
	w  io.Writer
	ch chan<- []byte
}

func (a *auditTapWriter) Write(p []byte) (int, error) {
	if a != nil && a.ch != nil && len(p) > 0 {
		chunk := make([]byte, len(p))
		copy(chunk, p)
		select {
		case a.ch <- chunk:
		default:
		}
	}
	return a.w.Write(p)
}

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

// wsStdinAuditor parses a stream of RFC 6455 WebSocket frames flowing from
// the client toward the kube-apiserver and emits one audit log entry per
// stdin payload (the K8s remotecommand channel 0).
//
// It is intentionally tolerant: malformed input is dropped silently so a
// crafted client cannot poison the audit goroutine and starve the proxy.
//
// The parser is single-goroutine; callers feed it bytes via Feed(). Frames
// can straddle Feed() calls because internal state survives between calls.
type wsStdinAuditor struct {
	logger *slog.Logger
	meta   sessionMeta
	buf    []byte
	// lineBuf accumulates stdin bytes until newline / carriage return so
	// the audit log records whole commands rather than 1-byte keystrokes.
	lineBuf []byte
}

type sessionMeta struct {
	RequestID string
	ClusterID string
	Subject   string
	Namespace string
	Pod       string
	Container string
}

func newWSStdinAuditor(logger *slog.Logger, meta sessionMeta) *wsStdinAuditor {
	if logger == nil {
		logger = slog.Default()
	}
	return &wsStdinAuditor{logger: logger, meta: meta}
}

// Feed appends bytes to the parser. It never errors; callers should drop
// bytes silently if Feed cannot keep up (the proxy must not block).
func (a *wsStdinAuditor) Feed(p []byte) {
	if a == nil || len(p) == 0 {
		return
	}
	a.buf = append(a.buf, p...)
	for {
		consumed, ok := a.tryConsume()
		if !ok {
			break
		}
		a.buf = a.buf[consumed:]
	}
	// keep the working buffer bounded; a single frame > 1 MiB is almost
	// certainly garbage or a control-channel resize, drop it.
	if len(a.buf) > 1<<20 {
		a.buf = a.buf[:0]
	}
}

// Flush emits whatever stdin bytes are buffered so the closing audit entry
// captures any partially typed line the user did not commit with Enter.
func (a *wsStdinAuditor) Flush() {
	if a == nil {
		return
	}
	if len(a.lineBuf) > 0 {
		a.emit(a.lineBuf, true)
		a.lineBuf = a.lineBuf[:0]
	}
}

func (a *wsStdinAuditor) tryConsume() (int, bool) {
	if len(a.buf) < 2 {
		return 0, false
	}

	b0 := a.buf[0]
	b1 := a.buf[1]
	opcode := b0 & 0x0F
	masked := b1&0x80 != 0
	payloadLen := int(b1 & 0x7F)
	pos := 2

	switch payloadLen {
	case 126:
		if len(a.buf) < pos+2 {
			return 0, false
		}
		payloadLen = int(binary.BigEndian.Uint16(a.buf[pos : pos+2]))
		pos += 2
	case 127:
		if len(a.buf) < pos+8 {
			return 0, false
		}
		n := binary.BigEndian.Uint64(a.buf[pos : pos+8])
		// guard against insane lengths; an exec stdin frame is always tiny.
		if n > 1<<20 {
			return 0, false
		}
		payloadLen = int(n)
		pos += 8
	}

	var maskKey [4]byte
	if masked {
		if len(a.buf) < pos+4 {
			return 0, false
		}
		copy(maskKey[:], a.buf[pos:pos+4])
		pos += 4
	}

	if len(a.buf) < pos+payloadLen {
		return 0, false
	}

	// copy then unmask so we never mutate caller-owned memory.
	payload := make([]byte, payloadLen)
	copy(payload, a.buf[pos:pos+payloadLen])
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i&3]
		}
	}

	// Only binary frames carry K8s remotecommand channel data. Channel 0
	// is stdin; we ignore resize (4) and others.
	if opcode == 0x2 && len(payload) >= 1 && payload[0] == 0 {
		a.consumeStdin(payload[1:])
	}

	return pos + payloadLen, true
}

func (a *wsStdinAuditor) consumeStdin(data []byte) {
	for _, b := range data {
		switch b {
		case '\r', '\n':
			if len(a.lineBuf) > 0 {
				a.emit(a.lineBuf, false)
				a.lineBuf = a.lineBuf[:0]
			}
		default:
			a.lineBuf = append(a.lineBuf, b)
			if len(a.lineBuf) >= 4096 {
				a.emit(a.lineBuf, true)
				a.lineBuf = a.lineBuf[:0]
			}
		}
	}
}

func (a *wsStdinAuditor) emit(line []byte, truncated bool) {
	display := sanitizeForLog(line)
	a.logger.Info("kapi exec stdin",
		"request_id", a.meta.RequestID,
		"cluster_id", a.meta.ClusterID,
		"subject", a.meta.Subject,
		"namespace", a.meta.Namespace,
		"pod", a.meta.Pod,
		"container", a.meta.Container,
		"len", len(line),
		"truncated", truncated,
		"input", display,
	)
}

// sanitizeForLog produces a single-line, printable representation of raw
// stdin bytes. Control codes (arrow keys, Esc sequences, Ctrl-C, etc.) are
// rendered as their hex escape so log scrapers stay safe. Non-UTF-8 input
// is rendered byte-by-byte.
func sanitizeForLog(p []byte) string {
	if !utf8.Valid(p) {
		var b strings.Builder
		b.Grow(len(p) * 4)
		for _, c := range p {
			if c < 0x20 || c == 0x7f {
				b.WriteString(`\x`)
				const hex = "0123456789abcdef"
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0x0f])
			} else {
				b.WriteByte(c)
			}
		}
		return b.String()
	}
	var b strings.Builder
	b.Grow(len(p))
	for _, r := range string(p) {
		switch {
		case r == 0x7f:
			b.WriteString(`\x7f`)
		case unicode.IsPrint(r) || r == ' ' || r == '\t':
			b.WriteRune(r)
		default:
			b.WriteString(`\x`)
			const hex = "0123456789abcdef"
			b.WriteByte(hex[byte(r)>>4])
			b.WriteByte(hex[byte(r)&0x0f])
		}
	}
	return b.String()
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
