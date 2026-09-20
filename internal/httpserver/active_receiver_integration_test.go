package httpserver

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestActiveReceiverHTTPFortyRootsPoolAndIdleArrival(t *testing.T) {
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
	server := imapserver.New(&imapserver.Options{Logger: log.New(io.Discard, "", 0),
		Caps: imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIdle: {}},
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &demandIMAPSession{Session: backend.NewSession(), trace: trace}, nil, nil
		}})
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = listener.Close(); _ = server.Close(); <-done })
	password, err := env.cipher.Encrypt("fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	account, err := env.store.CreateAccount(ctx, domain.Account{MailboxType: domain.MailboxTypeICloud,
		Email: "owner@icloud.com", IMAPHost: "127.0.0.1", IMAPPort: listener.Addr().(*net.TCPAddr).Port,
		IMAPUsername: "owner@example.test", PasswordCiphertext: password, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	aliases := make([]domain.Alias, 40)
	paths := make([]string, 40)
	for i := range aliases {
		aliases[i], _ = createV2AliasFixture(t, env, account.ID, fmt.Sprintf("root-%d@icloud.com", i))
		dto, err := env.server.adminAPIAliasFromDomain(aliases[i])
		if err != nil {
			t.Fatal(err)
		}
		paths[i] = dto.OTPURLPath
	}
	if err := env.store.EnrollPoolAliases(ctx, []int64{aliases[0].ID}); err != nil {
		t.Fatal(err)
	}
	client, key, err := env.store.CreatePoolClient(ctx, "active-receiver-fixture")
	if err != nil {
		t.Fatal(err)
	}
	leases, err := env.store.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "active-receiver-pool-0001", Count: 1, TTLSeconds: 600})
	if err != nil || len(leases) != 1 {
		t.Fatalf("lease: %v", err)
	}
	// Claim rotates credentials; use the current root link, not the pre-claim one.
	aliases[0], err = env.store.GetAlias(ctx, aliases[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	dto, err := env.server.adminAPIAliasFromDomain(aliases[0])
	if err != nil {
		t.Fatal(err)
	}
	paths[0] = dto.OTPURLPath
	appendMail := func(address, code string, at time.Time) {
		t.Helper()
		raw := fmt.Sprintf("From: sender@example.test\r\nX-Original-To: %s\r\nDelivered-To: owner@example.test\r\nTo: %s\r\nSubject: Verification code\r\nContent-Type: text/plain\r\n\r\nYour verification code is %s\r\n", address, address, code)
		if _, err := user.Append("INBOX", strings.NewReader(raw), &imap.AppendOptions{Time: at}); err != nil {
			t.Fatal(err)
		}
	}
	appendMail("root-0@icloud.com", "888888", time.Now().Add(-24*time.Hour))
	appendMail("root-0+wrong@other.test", "777777", time.Now())
	for i := range aliases {
		appendMail(fmt.Sprintf("root-%d+task@icloud.com", i), fmt.Sprintf("%06d", 100000+i), time.Now().Add(time.Second))
	}
	fetcher := mailfetch.NewFetcher()
	fetcher.MaxIncrementalCandidates = 128
	fetcher.ArchiveTempDir = env.store.MailArchiveTempDir()
	manager := syncer.New(env.store, env.cipher, fetcher, env.server.logger, time.Minute, 2)
	manager.SetOnDemandOnly(true)
	receiver := syncer.NewActiveReceiver(ctx, manager, fetcher.WatchMailbox)
	t.Cleanup(receiver.Close)
	env.server.SetSharedMailboxReceiver(receiver.SyncAlias)
	waitSubscribed := make(chan struct{}, 128)
	env.server.SetMailboxChanges(func(id int64) (<-chan struct{}, time.Duration) {
		ch, delay := receiver.MailboxChanges(id)
		select {
		case waitSubscribed <- struct{}{}:
		default:
		}
		return ch, delay
	})
	now := time.Now()
	env.server.now = func() time.Time { return now }
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			response := serveV2Request(router, http.MethodGet, path, "", nil)
			want := fmt.Sprintf(`"otp":"%06d"`, 100000+i)
			if response.Code != 200 || !strings.Contains(response.Body.String(), want) || strings.Contains(response.Body.String(), "888888") || strings.Contains(response.Body.String(), "777777") {
				t.Errorf("root %d: %d %s", i, response.Code, response.Body.String())
			}
		}(i, path)
	}
	wg.Wait()
	trace.mu.Lock()
	if len(trace.bodies) != 40 {
		t.Errorf("body downloads=%d, want exactly 40", len(trace.bodies))
	}
	if trace.logins > 3 {
		t.Errorf("initial logins=%d, want at most 3 including IDLE", trace.logins)
	}
	trace.mu.Unlock()
	now = now.Add(3 * time.Second)
	response := serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+leases[0].ID+"/code", "", map[string]string{"Authorization": "Bearer " + key})
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"otp":"100000"`) {
		t.Fatalf("pool: %d %s", response.Code, response.Body.String())
	}
	duplicate := serveV2Request(router, http.MethodGet, paths[0], "", nil)
	if duplicate.Code != 429 || duplicate.Header().Get("Retry-After") != "2" {
		t.Fatal("root URL and pool lost shared cooldown")
	}

	// New mail must reach the archive through real TLS IDLE, without API polling.
	appendMail("root-0+next@icloud.com", "654321", time.Now().Add(2*time.Second))
	deadline := time.Now().Add(5 * time.Second)
	for {
		records, err := env.store.ListAliasOTPs(ctx, aliases[0].ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) == 1 && records[0].OTP == "654321" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("IDLE did not publish new mail without a polling request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	now = now.Add(3 * time.Second)
	start := time.Now()
	response = serveV2Request(router, http.MethodGet, paths[0], "", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"otp":"654321"`) {
		t.Fatalf("new OTP: %d %s", response.Code, response.Body.String())
	}
	t.Logf("warm fixture pickup: %s", time.Since(start))
	trace.mu.Lock()
	if len(trace.bodies) != 41 {
		t.Fatalf("new mail body downloads=%d, want exactly 41", len(trace.bodies))
	}
	trace.mu.Unlock()

	// A waiting Pool reader gets a later +tag message from the shared IDLE
	// receiver without issuing another HTTP request or logging into IMAP again.
	now = now.Add(3 * time.Second)
	after := time.Now().Add(3 * time.Second)
	waitResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		waitResult <- serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+leases[0].ID+"/code?wait_seconds=5&after="+url.QueryEscape(after.Format(time.RFC3339Nano)), "", map[string]string{"Authorization": "Bearer " + key})
	}()
	select {
	case <-waitSubscribed:
	case <-time.After(3 * time.Second):
		t.Fatal("Pool reader did not subscribe")
	}
	appendMail("root-0+waiting@icloud.com", "456789", after.Add(time.Second))
	select {
	case response := <-waitResult:
		if response.Code != 200 || !strings.Contains(response.Body.String(), `"otp":"456789"`) {
			t.Fatalf("waiting Pool result: %d %s", response.Code, response.Body.String())
		}
	case <-time.After(6 * time.Second):
		t.Fatal("waiting Pool reader missed IDLE arrival")
	}
	trace.mu.Lock()
	if trace.logins != 2 || len(trace.bodies) != 42 {
		t.Fatalf("shared connection trace: logins=%d bodies=%d, want 2/42", trace.logins, len(trace.bodies))
	}
	trace.mu.Unlock()

	ids := make([]int64, 0, 39)
	for _, alias := range aliases[1:] {
		ids = append(ids, alias.ID)
	}
	if err := env.store.EnrollPoolAliases(ctx, ids); err != nil {
		t.Fatal(err)
	}
	more, err := env.store.ClaimPool(ctx, client.ID, store.PoolClaim{RequestID: "active-receiver-pool-0040", Count: 39, TTLSeconds: 600})
	if err != nil || len(more) != 39 {
		t.Fatalf("parallel leases: %v count=%d", err, len(more))
	}
	allLeases := append(leases, more...)
	now = now.Add(3 * time.Second)
	after = time.Now().Add(6 * time.Second)
	results := make(chan *httptest.ResponseRecorder, 40)
	for _, lease := range allLeases {
		go func(lease store.PoolLease) {
			results <- serveV2Request(router, http.MethodGet, "/api/v1/pool/leases/"+lease.ID+"/code?wait_seconds=5&after="+url.QueryEscape(after.Format(time.RFC3339Nano)), "", map[string]string{"Authorization": "Bearer " + key})
		}(lease)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		env.server.aliasDemandRateMu.Lock()
		active := env.server.accountPickupActive[account.ID]
		env.server.aliasDemandRateMu.Unlock()
		if active == 40 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("concurrent waiting readers=%d, want 40", active)
		}
		select {
		case <-waitSubscribed:
		case <-time.After(20 * time.Millisecond):
		}
	}
	// One arrival must unblock only its root; the other 39 remain waiting.
	appendMail("root-0+wave-a@icloud.com", "200000", after.Add(time.Second))
	select {
	case r := <-results:
		if r.Code != 200 || !strings.Contains(r.Body.String(), `"otp":"200000"`) {
			t.Fatalf("first staggered root: %d %s", r.Code, r.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("first staggered arrival missed")
	}
	for i := 1; i < 40; i++ {
		appendMail(fmt.Sprintf("root-%d+wave-b@icloud.com", i), fmt.Sprintf("%06d", 200000+i), after.Add(time.Second))
	}
	for i := 1; i < 40; i++ {
		select {
		case r := <-results:
			if r.Code != 200 || !strings.Contains(r.Body.String(), `"success":true`) {
				t.Fatalf("staggered wait: %d %s", r.Code, r.Body.String())
			}
		case <-time.After(6 * time.Second):
			t.Fatal("parallel wait missed a committed arrival")
		}
	}
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.logins != 2 || len(trace.bodies) != 82 {
		t.Fatalf("forty waiters logins=%d bodies=%d, want 2/82", trace.logins, len(trace.bodies))
	}
}
