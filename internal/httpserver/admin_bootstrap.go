package httpserver

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"icloud-api/internal/secure"
)

// Reuse the authenticated session DTO, not the cookie, to remove a serial
// session HTTP round trip on refresh. Every subsequent API still authenticates.
func (s *Server) adminSessionBootstrap(c *gin.Context) string {
	view := adminViewForPath(strings.TrimPrefix(c.Request.URL.Path, s.cfg.AdminPath))
	if view == "" || view == "LoginView" {
		return ""
	}
	raw, err := c.Cookie(sessionCookie)
	if err != nil || raw == "" {
		return ""
	}
	// Optional acceleration must not hold up the HTML on a busy database or
	// credential rotation. The normal session API remains the fallback.
	if !s.credentialRotationMu.TryRLock() {
		return ""
	}
	defer s.credentialRotationMu.RUnlock()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 200*time.Millisecond)
	defer cancel()
	session, err := s.store.GetSessionByHash(ctx, secure.HashToken(raw))
	if err != nil {
		return ""
	}
	data, err := json.Marshal(adminAPISessionFromDomain(session))
	if err != nil {
		return ""
	}
	// encoding/json escapes '<', including any user-controlled </script> text.
	return `<script type="application/json" id="icloud-admin-session">` + string(data) + `</script>`
}
