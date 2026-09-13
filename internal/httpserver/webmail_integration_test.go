package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/hmesync"
	mailfetch "icloud-api/internal/mail"
	"icloud-api/internal/store"
	"icloud-api/internal/syncer"
)

type webMailRoundTrip func(*http.Request) (*http.Response, error)

func (f webMailRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebMailDemandURLReadsTargetWithoutIMAPOrCursorMutation(t *testing.T) {
	env := newAdminAPITestEnv(t)
	ctx := context.Background()
	if err := env.store.ConfigureMailArchive(t.TempDir(), 8<<20); err != nil {
		t.Fatal(err)
	}
	// Deliberately undecryptable IMAP material proves this path never uses it.
	account, err := env.store.CreateAccount(ctx, domain.Account{Email: "owner@icloud.com", IMAPUsername: "owner@icloud.com", IMAPHost: domain.DefaultIMAPHost, IMAPPort: 993, PasswordCiphertext: "not-an-imap-credential", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := createV2AliasFixture(t, env, account.ID, "target@icloud.com")
	b, _ := createV2AliasFixture(t, env, account.ID, "other@icloud.com")
	if err := env.store.RecordMailboxSyncFailure(ctx, account.ID, account.UpdatedAt, "imap: NO [AUTHENTICATIONFAILED] Authentication Failed", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := env.store.SetAccountMailTransport(ctx, account.ID, store.MailTransportWebmail); err != nil {
		t.Fatal(err)
	}
	session := apple.Session{AppleID: account.Email, DSID: "fixture-dsid", Region: apple.RegionGlobal, MailGatewayURL: "https://p01-mccgateway.icloud.com", ValidatedAt: time.Now(), Cookies: []apple.PersistentCookie{{Name: "X-APPLE-WEBAUTH-TOKEN", Value: "fixture-web", Domain: "icloud.com", Path: "/", Secure: true}}}
	payload, _ := json.Marshal(session)
	encrypted, err := env.cipher.EncryptAppleSession(string(payload))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.store.UpsertAppleWebSession(ctx, domain.AppleWebSession{AccountID: account.ID, AppleID: account.Email, Region: "global", Authenticated: true, Ciphertext: encrypted}); err != nil {
		t.Fatal(err)
	}
	calls, bodies := 0, 0
	client, err := apple.NewClient(apple.Config{Transport: webMailRoundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		var body any
		switch r.URL.Path {
		case "/mailws2/v1/geqs/query":
			body = map[string]any{"domainObjects": []any{map[string]any{"identifier": "INBOX", "name": "INBOX", "uidValidity": 77}}}
		case "/mailws2/v1/thread/search":
			body = map[string]any{"threadList": []any{map[string]any{"threadId": "fixture-thread"}}}
		case "/mailws2/v1/thread/get":
			body = map[string]any{"messageMetadataList": []any{
				map[string]any{"uid": 7, "folder": "INBOX", "date": time.Now().Add(-time.Minute).UnixMilli(), "to": []any{map[string]string{"email": a.Address}}, "parts": []any{map[string]any{"partId": "1", "contentType": "text/plain"}}},
				map[string]any{"uid": 8, "folder": "INBOX", "to": []any{map[string]string{"email": b.Address}}, "parts": []any{map[string]any{"partId": "1", "contentType": "text/plain"}}},
			}}
		case "/mailws2/v1/message/get":
			bodies++
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request["dontMarkAsRead"] != true || request["uid"] != "7" {
				t.Fatalf("wrong target/flags: %v", request)
			}
			body = map[string]any{"longHeader": "From: sender@example.test\r\nTo: target@icloud.com\r\nX-Original-To: target@icloud.com\r\nSubject: Verification code\r\nMessage-Id: <fixture-web@example.test>\r\nDate: " + time.Now().Add(-time.Minute).Format(time.RFC1123Z), "parts": []any{map[string]string{"content": "Verification code: 123456"}}}
		default:
			t.Fatalf("unexpected upstream operation: %s", r.URL.Path)
		}
		data, _ := json.Marshal(body)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(data))), Request: r}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	fetcher := mailfetch.NewFetcher()
	manager := syncer.New(env.store, env.cipher, fetcher, env.server.logger, time.Minute, 1)
	manager.SetOnDemandOnly(true)
	service, err := hmesync.New(env.store, env.cipher, client, manager)
	if err != nil {
		t.Fatal(err)
	}
	manager.SetWebMailFetcher(func(ctx context.Context, account domain.Account, alias domain.Alias, known []domain.Alias) (domain.MailboxSyncResult, error) {
		remote, err := service.ReadAliasWebMailLocked(ctx, account, alias)
		if err != nil {
			return domain.MailboxSyncResult{}, err
		}
		return fetcher.ArchiveWebMail(ctx, account, alias, known, remote)
	})
	env.server.SetAliasDemandSync(manager.SyncAliasOnDemand)
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	dto, err := env.server.adminAPIAliasFromDomain(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dto.OTPURLPath, dto.DirectLinkPath} {
		r := serveV2Request(router, http.MethodGet, path, "", nil)
		if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "123456") {
			t.Fatalf("read status=%d body=%s", r.Code, r.Body.String())
		}
	}
	if calls != 4 || bodies != 1 {
		t.Fatalf("cache/target fetch calls=%d bodies=%d", calls, bodies)
	}
	for _, id := range []int64{a.ID, b.ID} {
		if _, err := env.store.GetAliasIMAPSyncState(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("IMAP cursor written: %v", err)
		}
	}
	if _, err := env.store.GetIMAPSyncState(ctx, account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("account IMAP cursor written: %v", err)
	}
	if otps, err := env.store.ListAliasOTPs(ctx, b.ID, 10); err != nil || len(otps) > 0 {
		t.Fatalf("other alias received target content: %v", err)
	}
	current, err := env.store.GetAccount(ctx, account.ID)
	if err != nil || current.LastSyncError != "" || current.LastSyncStatus != domain.SyncStatusOK {
		t.Fatalf("health not recovered: %v", err)
	}
	if got, err := env.store.GetAccountMailTransport(ctx, account.ID); err != nil || got != "webmail" {
		t.Fatal(fmt.Sprint(got, err))
	}
}
