package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type CORSConfig struct {
	AllowedOrigins   []string
	AllowCredentials bool
	AllowHeaders     []string
	AllowMethods     []string
}

func CORSHTTP(cfg CORSConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		applyCORS(w.Header(), cfg, r.Header.Get("Origin"))
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func CORSGin(cfg CORSConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		applyCORS(c.Writer.Header(), cfg, c.Request.Header.Get("Origin"))
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func applyCORS(header http.Header, cfg CORSConfig, origin string) {
	if len(cfg.AllowedOrigins) == 0 {
		return
	}

	allowedOrigin := "*"
	for _, candidate := range cfg.AllowedOrigins {
		if candidate == "*" || candidate == origin {
			allowedOrigin = candidate
			break
		}
	}

	header.Set("Access-Control-Allow-Origin", allowedOrigin)
	header.Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowHeaders, ", "))
	header.Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowMethods, ", "))
	if cfg.AllowCredentials {
		header.Set("Access-Control-Allow-Credentials", "true")
	}
}
