package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/kubeproxy/application"
	configpkg "github.com/lanyulei/kubeflare/internal/platform/config"
)

type TransportPool struct {
	base  *http.Transport
	cache sync.Map
	size  atomic.Int64
}

const maxCachedTransports = 512

func NewTransportPool(cfg configpkg.ProxyConfig) *TransportPool {
	return &TransportPool{
		base: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ForceAttemptHTTP2:     true,
			DialContext:           (&net.Dialer{Timeout: cfg.DialTimeout, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          cfg.MaxIdleConns,
			MaxIdleConnsPerHost:   cfg.MaxIdleConnsPerHost,
			MaxConnsPerHost:       cfg.MaxConnsPerHost,
			IdleConnTimeout:       cfg.IdleConnTimeout,
			TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
			ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		},
	}
}

func (p *TransportPool) For(target application.ClusterTarget) (http.RoundTripper, error) {
	key := transportCacheKey(target)
	if transport, ok := p.cache.Load(key); ok {
		return transport.(http.RoundTripper), nil
	}

	transport, err := NewTransport(p.base, target)
	if err != nil {
		return nil, err
	}
	actual, loaded := p.cache.LoadOrStore(key, transport)
	if loaded {
		if closable, ok := transport.(*http.Transport); ok {
			closable.CloseIdleConnections()
		}
		return actual.(http.RoundTripper), nil
	}
	if p.size.Add(1) > maxCachedTransports {
		p.clearCachedTransports()
	}
	return transport, nil
}

func transportCacheKey(target application.ClusterTarget) string {
	certHash := sha256.Sum256([]byte(target.CACertPEM))
	return target.ID + "|" + target.BaseURL.Host + "|" + target.TLSServerName + "|" + hex.EncodeToString(certHash[:]) + "|" + boolString(target.SkipTLSVerify)
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func (p *TransportPool) CloseIdleConnections() {
	if p == nil {
		return
	}

	p.base.CloseIdleConnections()
	p.closeCachedTransports(false)
}

func (p *TransportPool) clearCachedTransports() {
	p.closeCachedTransports(true)
}

func (p *TransportPool) closeCachedTransports(deleteEntries bool) {
	p.cache.Range(func(key, value any) bool {
		if transport, ok := value.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
		if deleteEntries {
			p.cache.Delete(key)
		}
		return true
	})
	if deleteEntries {
		p.size.Store(0)
	}
}
