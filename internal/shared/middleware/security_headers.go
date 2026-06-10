package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 安全响应头采用保守、与 API/代理场景无冲突的默认值:
//   - X-Content-Type-Options: nosniff —— 禁止浏览器 MIME 嗅探,防止把 JSON/文本
//     当作可执行内容。
//   - X-Frame-Options: DENY —— 禁止页面被嵌入 iframe,防点击劫持。
//   - Referrer-Policy: no-referrer —— 跨域跳转不泄露完整 URL(可能含敏感路径)。
//
// 有意不设置 HSTS 与 CSP:HSTS 需确认全链路 TLS 才安全,CSP 取决于前端资源策略,
// 二者误设会造成线上故障,应由部署方按环境显式开启,而非在此硬编码。
var securityHeaders = [...][2]string{
	{"X-Content-Type-Options", "nosniff"},
	{"X-Frame-Options", "DENY"},
	{"Referrer-Policy", "no-referrer"},
}

// SecurityHeadersHTTP 为所有响应附加基础安全头(net/http 版本)。
func SecurityHeadersHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		applySecurityHeaders(w.Header())
		next.ServeHTTP(w, r)
	})
}

// SecurityHeadersGin 为所有响应附加基础安全头(gin 版本)。
func SecurityHeadersGin() gin.HandlerFunc {
	return func(c *gin.Context) {
		applySecurityHeaders(c.Writer.Header())
		c.Next()
	}
}

func applySecurityHeaders(header http.Header) {
	for _, kv := range securityHeaders {
		// 不覆盖下游已显式设置的同名头(如个别接口可能需要 SAMEORIGIN 以支持内嵌)。
		if header.Get(kv[0]) == "" {
			header.Set(kv[0], kv[1])
		}
	}
}
