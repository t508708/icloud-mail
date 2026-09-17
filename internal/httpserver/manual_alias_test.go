package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/hmesync"
)

type manualAliasFake struct {
	fakeHMESyncService
	create  func(context.Context, int64) (domain.Alias, error)
	channel string
}

func (f *manualAliasFake) ProbeAliasWithChannel(ctx context.Context, id int64, channel string) (domain.Alias, error) {
	f.channel = channel
	return f.create(ctx, id)
}

func manualAliasRequest(t *testing.T, env *adminAPITestEnv, accountID int64, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	return env.request(t, http.MethodPost, fmt.Sprintf("/admin/api/v1/accounts/%d/aliases/create-now", accountID), []byte(`{}`), "application/json", []*http.Cookie{cookie}, csrf)
}

func TestManualAliasRequiresSessionAndCSRF(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "manual-auth@icloud.com")
	called := false
	env.server.SetHMESyncService(&manualAliasFake{create: func(context.Context, int64) (domain.Alias, error) { called = true; return domain.Alias{}, nil }})
	path := fmt.Sprintf("/admin/api/v1/accounts/%d/aliases/create-now", account.ID)
	withoutSession := env.request(t, http.MethodPost, path, []byte(`{}`), "application/json", nil, "")
	if withoutSession.Code != http.StatusUnauthorized || called {
		t.Fatalf("without session = %d, called=%v", withoutSession.Code, called)
	}
	cookie, _, _ := env.createSession(t, "manual-auth-admin", "password")
	withoutCSRF := env.request(t, http.MethodPost, path, []byte(`{}`), "application/json", []*http.Cookie{cookie}, "")
	if withoutCSRF.Code != http.StatusForbidden || called {
		t.Fatalf("without csrf = %d, called=%v", withoutCSRF.Code, called)
	}
}

func TestManualAliasCreatesWhenAutoScheduleDoesNotMatter(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "manual-create@icloud.com")
	// Seed a realistic exhausted/just-attempted automatic schedule. The manual
	// endpoint must not consult or wait on this scheduler state.
	now := time.Now().UTC()
	planned := []time.Time{now.Add(5 * time.Minute), now.Add(10 * time.Minute), now.Add(15 * time.Minute), now.Add(20 * time.Minute), now.Add(25 * time.Minute)}
	if err := env.store.EnableAliasCreation(context.Background(), account.ID, planned, now); err != nil {
		t.Fatal(err)
	}
	if err := env.store.RecordAliasCreationFailure(context.Background(), account.ID, now, "recent attempt"); err != nil {
		t.Fatal(err)
	}
	alias, credentials := createV2AliasFixture(t, env, account.ID, "manual-created@icloud.com")
	var calls int
	env.server.SetHMESyncService(&manualAliasFake{create: func(context.Context, int64) (domain.Alias, error) { calls++; return alias, nil }})
	cookie, csrf, _ := env.createSession(t, "manual-create-admin", "password")
	response := manualAliasRequest(t, env, account.ID, cookie, csrf)
	if response.Code != http.StatusCreated || calls != 1 {
		t.Fatalf("response=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
	var envelope struct {
		Data struct {
			Alias struct {
				Address      string `json:"address"`
				APIKey       string `json:"api_key"`
				IMAPPassword string `json:"imap_password"`
				ClientID     string `json:"client_id"`
				RefreshToken string `json:"refresh_token"`
			} `json:"alias"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Alias.Address != alias.Address || envelope.Data.Alias.APIKey != credentials.APIKey {
		t.Fatalf("payload=%+v", envelope.Data.Alias)
	}
	if envelope.Data.Alias.IMAPPassword != credentials.IMAPPassword || envelope.Data.Alias.ClientID != credentials.ClientID || envelope.Data.Alias.RefreshToken != credentials.RefreshToken {
		t.Fatal("response credentials differ from the stored bundle")
	}
}

func TestManualAliasRateLimitAndBusyRelease(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "manual-rate@icloud.com")
	alias, _ := createV2AliasFixture(t, env, account.ID, "manual-rate-alias@icloud.com")
	cookie, csrf, _ := env.createSession(t, "manual-rate-admin", "password")
	var mu sync.Mutex
	calls := 0
	fail := true
	fake := &manualAliasFake{create: func(context.Context, int64) (domain.Alias, error) {
		mu.Lock()
		calls++
		current := fail
		mu.Unlock()
		if current {
			return domain.Alias{}, errors.Join(hmesync.ErrRateLimited, &apple.Error{Kind: apple.ErrService, StatusCode: http.StatusTooManyRequests, RetryAfter: 7 * time.Second})
		}
		return alias, nil
	}}
	env.server.SetHMESyncService(fake)
	first := manualAliasRequest(t, env, account.ID, cookie, csrf)
	if first.Code != http.StatusTooManyRequests || first.Header().Get("Retry-After") != "7" {
		t.Fatalf("rate response=%d retry=%q", first.Code, first.Header().Get("Retry-After"))
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	second := manualAliasRequest(t, env, account.ID, cookie, csrf)
	if second.Code != http.StatusCreated {
		t.Fatalf("after failure=%d body=%s", second.Code, second.Body.String())
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 2 {
		t.Fatalf("calls=%d", got)
	}
}

func TestManualProbeChannelAndWaitingBatchAreIndependent(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "probe-batch@icloud.com")
	alias, _ := createV2AliasFixture(t, env, account.ID, "probe-batch-created@icloud.com")
	cookie, csrf, _ := env.createSession(t, "probe-batch-admin", "password")
	calls := 0
	fake := &manualAliasFake{create: func(context.Context, int64) (domain.Alias, error) { calls++; return alias, nil }}
	env.server.SetHMESyncService(fake)
	env.server.manualAliasesRunning = map[int64]bool{account.ID: true}
	path := fmt.Sprintf("/admin/api/v1/accounts/%d/aliases/create-now", account.ID)
	for _, channel := range []string{"auto", "apple_account", "icloud_web"} {
		response := env.request(t, http.MethodPost, path, adminAPITestJSON(t, map[string]string{"channel": channel}), "application/json", []*http.Cookie{cookie}, csrf)
		if response.Code != http.StatusCreated || fake.channel != channel {
			t.Fatalf("probe channel=%s got=%s response=%d", channel, fake.channel, response.Code)
		}
		if !env.server.manualAliasesRunning[account.ID] {
			t.Fatal("probe cleared batch ownership")
		}
	}
	response := env.request(t, http.MethodPost, path, []byte(`{"channel":"invalid"}`), "application/json", []*http.Cookie{cookie}, csrf)
	if response.Code != http.StatusBadRequest || calls != 3 {
		t.Fatalf("invalid probe: %d calls=%d", response.Code, calls)
	}
}

func TestManualAliasIMAPAuthenticationPauseIsConflict(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "manual-imap-paused@icloud.com")
	cookie, csrf, _ := env.createSession(t, "manual-imap-paused-admin", "password")
	env.server.SetHMESyncService(&manualAliasFake{create: func(context.Context, int64) (domain.Alias, error) {
		return domain.Alias{}, domain.ErrIMAPAuthenticationPaused
	}})
	response := manualAliasRequest(t, env, account.ID, cookie, csrf)
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusConflict || payload.Error.Code != "IMAP_AUTHENTICATION_PAUSED" {
		t.Fatalf("IMAP pause response=%d body=%s", response.Code, response.Body.String())
	}
}

func TestManualAliasConcurrentRequestReturnsBusy(t *testing.T) {
	env := newAdminAPITestEnv(t)
	account := adminAPITestCreateAccount(t, env, "manual-busy@icloud.com")
	alias, _ := createV2AliasFixture(t, env, account.ID, "manual-busy-alias@icloud.com")
	cookie, csrf, _ := env.createSession(t, "manual-busy-admin", "password")
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	fake := &manualAliasFake{create: func(context.Context, int64) (domain.Alias, error) {
		calls++
		close(started)
		<-release
		return alias, nil
	}}
	env.server.SetHMESyncService(fake)
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- manualAliasRequest(t, env, account.ID, cookie, csrf) }()
	<-started
	second := manualAliasRequest(t, env, account.ID, cookie, csrf)
	if second.Code != http.StatusConflict || adminAPITestErrorCode(t, second) != "ALIAS_CREATION_BUSY" {
		t.Fatalf("busy=%d body=%s", second.Code, second.Body.String())
	}
	close(release)
	if first := <-result; first.Code != http.StatusCreated {
		t.Fatalf("first=%d", first.Code)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestManualAliasRejectsDisabledAndCustomAccounts(t *testing.T) {
	env := newAdminAPITestEnv(t)
	cookie, csrf, _ := env.createSession(t, "manual-gates-admin", "password")
	called := 0
	env.server.SetHMESyncService(&manualAliasFake{create: func(context.Context, int64) (domain.Alias, error) { called++; return domain.Alias{}, nil }})
	icloud := adminAPITestCreateAccount(t, env, "manual-disabled@icloud.com")
	icloud.Enabled = false
	if _, err := env.store.UpdateAccount(context.Background(), icloud); err != nil {
		t.Fatal(err)
	}
	disabled := manualAliasRequest(t, env, icloud.ID, cookie, csrf)
	if disabled.Code != http.StatusConflict || adminAPITestErrorCode(t, disabled) != "ACCOUNT_DISABLED" {
		t.Fatalf("disabled=%d body=%s", disabled.Code, disabled.Body.String())
	}
	custom, err := env.store.CreateAccount(context.Background(), domain.Account{Name: "custom", Email: "manual-custom@example.test", EmailSuffix: "example.test", MailboxType: domain.MailboxTypeCustom, IMAPHost: "imap.example.test", IMAPPort: 993, IMAPUsername: "manual-custom@example.test", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	customResponse := manualAliasRequest(t, env, custom.ID, cookie, csrf)
	if customResponse.Code != http.StatusConflict || adminAPITestErrorCode(t, customResponse) != "CUSTOM_MAILBOX_NO_APPLE" {
		t.Fatalf("custom=%d body=%s", customResponse.Code, customResponse.Body.String())
	}
	if called != 0 {
		t.Fatalf("creator called for rejected accounts: %d", called)
	}
}
