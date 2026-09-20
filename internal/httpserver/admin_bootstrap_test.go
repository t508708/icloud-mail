package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"icloud-api/internal/domain"
	"icloud-api/internal/secure"
)

func TestAdminSessionBootstrapAuthenticatedPrivateAndEscaped(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.cfg.AdminPath = "/custom/admin"
	env.server.adminSPA = &adminSPA{index: []byte(testAdminSPAIndex)}
	cookie, csrf, _ := env.createSession(t, `bootstrap</script><script>alert(1)</script>`, "fixture-password")
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	response := serveAdminRouterRequest(router, http.MethodGet, "/custom/admin/pool", nil, map[string]string{"Cookie": cookie.String()})
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store, private" || response.Header().Get("CDN-Cache-Control") != "no-store" || response.Header().Get("Vary") != "Cookie" {
		t.Fatalf("private HTML headers: status=%d headers=%v", response.Code, response.Header())
	}
	_, payload, found := strings.Cut(response.Body.String(), `<script type="application/json" id="icloud-admin-session">`)
	if !found {
		t.Fatal("authenticated HTML missing bootstrap")
	}
	payload, _, _ = strings.Cut(payload, "</script>")
	var data adminAPISessionDTO
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		t.Fatal(err)
	}
	if data.CSRFToken != csrf || data.Admin.Username != `bootstrap</script><script>alert(1)</script>` {
		t.Fatal("bootstrap session DTO mismatch")
	}
	for _, secret := range []string{cookie.Value, "fixture-password", "password_version", "token_hash", "<script>alert"} {
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("unexpected sensitive or executable content: %q", secret)
		}
	}
	for _, path := range []string{"/custom/admin/login", "/custom/admin/missing"} {
		response := serveAdminRouterRequest(router, http.MethodGet, path, nil, map[string]string{"Cookie": cookie.String()})
		if strings.Contains(response.Body.String(), "icloud-admin-session") {
			t.Fatalf("bootstrap exposed on %s", path)
		}
	}
}

func TestAdminSessionBootstrapRejectsMissingExpiredRevokedAndRotation(t *testing.T) {
	env := newAdminAPITestEnv(t)
	env.server.adminSPA = &adminSPA{index: []byte(testAdminSPAIndex)}
	cookie, _, admin := env.createSession(t, "bootstrap-admin", "fixture-password")
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	checkAbsent := func(method string, headers map[string]string) {
		t.Helper()
		r := serveAdminRouterRequest(router, method, "/admin/", nil, headers)
		if r.Code != 200 || strings.Contains(r.Body.String(), "icloud-admin-session") {
			t.Fatalf("unexpected bootstrap: %d", r.Code)
		}
	}
	checkAbsent(http.MethodGet, nil)
	checkAbsent(http.MethodGet, map[string]string{"Cookie": sessionCookie + "=invalid"})
	checkAbsent(http.MethodHead, map[string]string{"Cookie": cookie.String()})
	expired := "expired-bootstrap-token"
	if err := env.store.CreateSession(context.Background(), secure.HashToken(expired), domain.Session{
		AdminID: admin.ID, PasswordVersion: admin.PasswordVersion, CSRF: "expired-csrf", ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	checkAbsent(http.MethodGet, map[string]string{"Cookie": sessionCookie + "=" + expired})
	env.server.credentialRotationMu.Lock()
	checkAbsent(http.MethodGet, map[string]string{"Cookie": cookie.String()})
	env.server.credentialRotationMu.Unlock()
	if err := env.store.DeleteSession(context.Background(), secure.HashToken(cookie.Value)); err != nil {
		t.Fatal(err)
	}
	checkAbsent(http.MethodGet, map[string]string{"Cookie": cookie.String()})
	// A cached UI hint never replaces authentication of subsequent data APIs.
	r := serveAdminRouterRequest(router, http.MethodGet, "/admin/api/v1/accounts", nil, map[string]string{"Cookie": cookie.String()})
	if r.Code != 401 {
		t.Fatalf("revoked session API status: %d", r.Code)
	}
}
