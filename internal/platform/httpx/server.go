package httpx

import (
	"net/http"

	configpkg "github.com/lanyulei/kubeflare/internal/platform/config"
)

func NewServer(cfg configpkg.HTTPConfig, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              cfg.Address,
		Handler:           handler,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
}
