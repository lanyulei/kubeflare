package proxy

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/kubeproxy/application"
	configpkg "github.com/lanyulei/kubeflare/internal/platform/config"
)

type TransportPool struct {
	base  http.Transport
	cache sync.Map
}

func NewTransportPool(cfg configpkg.ProxyConfig) *TransportPool {
	return &TransportPool{
		base: http.Transport{
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
	key := target.ID + "|" + target.BaseURL.Host + "|" + target.TLSServerName
	if transport, ok := p.cache.Load(key); ok {
		return transport.(http.RoundTripper), nil
	}

	transport, err := NewTransport(p.base, target)
	if err != nil {
		return nil, err
	}
	p.cache.Store(key, transport)
	return transport, nil
}
