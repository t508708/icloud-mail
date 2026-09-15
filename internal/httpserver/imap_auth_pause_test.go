package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAdminSyncDoesNotQueuePersistedAuthenticationFailure(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "paused-sync@icloud.com")
	if _, err := env.store.DB().ExecContext(context.Background(),
		`UPDATE accounts SET last_sync_error = ? WHERE id = ?`,
		"login IMAP account: imap: BAD [AUTHENTICATIONFAILED] Authentication Failed", account.ID); err != nil {
		t.Fatal(err)
	}
	called := false
	env.server.sync = func(int64) error { called = true; return nil }
	cookie, csrf, _ := env.createSession(t, "paused-sync-admin", "password")
	response := env.request(t, http.MethodPost,
		fmt.Sprintf("/admin/api/v1/accounts/%d/sync", account.ID), nil, "", []*http.Cookie{cookie}, csrf)
	if called || response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "IMAP_AUTHENTICATION_PAUSED") {
		t.Fatalf("paused sync: queued=%v status=%d", called, response.Code)
	}
}
