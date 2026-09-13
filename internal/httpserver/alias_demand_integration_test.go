package httpserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	imap "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"icloud-api/internal/domain"
	mailfetch "icloud-api/internal/mail"
	"icloud-api/internal/store"
	"icloud-api/internal/syncer"
)

var demandFixtureTLS *tls.Config

func TestMain(m *testing.M) {
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	demandFixtureTLS = fixture.TLS.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	// The fixture trust applies only to this test process, never the host CA store.
	if err := os.Setenv("GODEBUG", os.Getenv("GODEBUG")+",x509usefallbackroots=1"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	x509.SetFallbackRoots(roots)
	os.Exit(m.Run())
}

type demandIMAPTrace struct {
	mu     sync.Mutex
	bodies []imap.UID
	logins int
}

type demandIMAPSession struct {
	imapserver.Session
	trace *demandIMAPTrace
}

func (s *demandIMAPSession) Login(username, password string) error {
	s.trace.mu.Lock()
	s.trace.logins++
	s.trace.mu.Unlock()
	return s.Session.Login(username, password)
}

func (s *demandIMAPSession) Fetch(w *imapserver.FetchWriter, set imap.NumSet, options *imap.FetchOptions) error {
	for _, section := range options.BodySection {
		if !section.Peek {
			return errors.New("fixture requires BODY.PEEK")
		}
		if section.Specifier == imap.PartSpecifierNone {
			uids, ok := set.(imap.UIDSet)
			if !ok {
				return errors.New("fixture requires UID FETCH")
			}
			values, _ := uids.Nums()
			s.trace.mu.Lock()
			s.trace.bodies = append(s.trace.bodies, values...)
			s.trace.mu.Unlock()
		}
	}
	return s.Session.Fetch(w, set, options)
}

func TestAliasDemandURLThroughIMAPAndStoreReturnsNewTargetOTP(t *testing.T) {
	env := newAdminAPITestEnv(t)
	ctx := context.Background()
	if err := env.store.ConfigureMailArchive(t.TempDir(), 64<<20); err != nil {
		t.Fatal(err)
	}
	user := imapmemserver.NewUser("owner@example.test", "fixture-password")
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	backend := imapmemserver.New()
	backend.AddUser(user)
	trace := &demandIMAPTrace{}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", demandFixtureTLS)
	if err != nil {
		t.Fatal(err)
	}
	imapServer := imapserver.New(&imapserver.Options{
		Logger: log.New(io.Discard, "", 0),
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &demandIMAPSession{Session: backend.NewSession(), trace: trace}, nil, nil
		},
	})
	done := make(chan struct{})
	go func() { defer close(done); _ = imapServer.Serve(listener) }()
	t.Cleanup(func() { _ = listener.Close(); _ = imapServer.Close(); <-done })
	password, err := env.cipher.Encrypt("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	account, err := env.store.CreateAccount(ctx, domain.Account{
		MailboxType: domain.MailboxTypeCustom, EmailSuffix: "example.test", Email: "owner@example.test",
		IMAPHost: "127.0.0.1", IMAPPort: listener.Addr().(*net.TCPAddr).Port,
		IMAPUsername: "owner@example.test", PasswordCiphertext: password, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	aliasA, _ := createV2AliasFixture(t, env, account.ID, "a@example.test")
	aliasB, keyB := createV2AliasFixture(t, env, account.ID, "b@example.test")
	for _, item := range []struct{ address, code string }{
		{"a@example.test", "123456"}, {"b@example.test", "654321"},
		{"nota@example.test", "777777"},
	} {
		raw := "From: sender@example.test\r\nX-Original-To: " + item.address + "\r\nTo: " + item.address + "\r\nSubject: Verification code\r\nContent-Type: text/plain\r\n\r\nYour verification code is " + item.code + "\r\n"
		if _, err := user.Append("INBOX", strings.NewReader(raw), &imap.AppendOptions{Time: time.Now().Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	fetcher := mailfetch.NewFetcher()
	fetcher.ArchiveTempDir = env.store.MailArchiveTempDir()
	manager := syncer.New(env.store, env.cipher, fetcher, env.server.logger, time.Minute, 1)
	manager.SetOnDemandOnly(true)
	env.server.SetAliasDemandSync(manager.SyncAliasOnDemand)
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	dto, err := env.server.adminAPIAliasFromDomain(aliasA)
	if err != nil {
		t.Fatal(err)
	}
	assertOTP := func(path, key, want, unwanted string) {
		t.Helper()
		headers := map[string]string{}
		if key != "" {
			headers["Authorization"] = "Bearer " + key
		}
		response := serveV2Request(router, http.MethodGet, path, "", headers)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) || strings.Contains(response.Body.String(), unwanted) {
			t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
	assertOTP(dto.OTPURLPath, "", "123456", "654321")
	if _, err := env.store.GetAliasIMAPSyncState(ctx, aliasB.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("untouched alias cursor changed: %v", err)
	}
	if rows, err := env.store.ListAliasOTPs(ctx, aliasB.ID, 10); err != nil || len(rows) != 0 {
		t.Fatalf("untouched alias archive changed: %v %v", rows, err)
	}
	assertOTP("/api/v1/otp", keyB.APIKey, "654321", "123456")
	assertOTP(dto.OTPURLPath, "", "123456", "654321")
	assertOTP(dto.DirectLinkPath, "", "123456", "654321")
	if _, err := env.store.GetIMAPSyncState(ctx, account.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("target fetch advanced account-wide cursor: %v", err)
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.logins != 2 || !reflect.DeepEqual(trace.bodies, []imap.UID{1, 2}) {
		t.Fatalf("login count=%d body UIDs=%v; want two logins and only [1 2]", trace.logins, trace.bodies)
	}
}
