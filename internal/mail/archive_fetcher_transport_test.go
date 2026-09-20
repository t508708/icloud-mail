package mail

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
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
)

var archiveFixtureTLSCertificate tls.Certificate

func TestMain(m *testing.M) {
	// Trust only this ephemeral fixture CA inside the test process. Production
	// TLS verification stays unchanged, and no system trust store is modified.
	certificate, roots, err := makeArchiveFixtureCertificate()
	if err != nil {
		fmt.Fprintln(os.Stderr, "create archive IMAP fixture certificate:", err)
		os.Exit(1)
	}
	archiveFixtureTLSCertificate = certificate
	debug := os.Getenv("GODEBUG")
	if debug != "" {
		debug += ","
	}
	if err := os.Setenv("GODEBUG", debug+"x509usefallbackroots=1"); err != nil {
		fmt.Fprintln(os.Stderr, "configure archive IMAP fixture trust:", err)
		os.Exit(1)
	}
	x509.SetFallbackRoots(roots)
	os.Exit(m.Run())
}

func makeArchiveFixtureCertificate() (tls.Certificate, *x509.CertPool, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "archive IMAP test fixture"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		IsCA:         true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, roots, nil
}

func TestArchiveIncrementalCandidateLimitDefaultsAndCompatibility(t *testing.T) {
	tests := []struct {
		name    string
		fetcher *Fetcher
		want    int
	}{
		{name: "nil defaults", want: 32},
		{name: "zero defaults", fetcher: &Fetcher{}, want: 32},
		{name: "constructor defaults", fetcher: NewFetcher(), want: 32},
		{name: "negative defaults", fetcher: &Fetcher{MaxIncrementalCandidates: -1}, want: 32},
		{name: "smaller override", fetcher: &Fetcher{MaxIncrementalCandidates: 8}, want: 8},
		{name: "legacy maximum override", fetcher: &Fetcher{MaxIncrementalCandidates: 256}, want: 256},
		{name: "hard maximum", fetcher: &Fetcher{MaxIncrementalCandidates: 1024}, want: 256},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settings := test.fetcher.settings()
			if settings.maxIncrementalCandidates != test.want {
				t.Fatalf("incremental candidate limit = %d, want %d", settings.maxIncrementalCandidates, test.want)
			}
			if settings.maxCandidates != 1024 {
				t.Fatalf("first-sync recent window = %d, want unchanged 1024", settings.maxCandidates)
			}
		})
	}
}

func TestArchiveIncrementalDefaultBatchesPreserveEveryUIDAndSeenFlags(t *testing.T) {
	fixture := startArchiveIMAPFixture(t, 65, 0)
	fetcher := NewFetcher()
	fetcher.ArchiveTempDir = t.TempDir()
	state := fixture.initialState(t)
	wantUID := uint32(1)
	for batch, wantCount := range []int{32, 32, 1} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		result, err := fetcher.FetchIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &state, nil)
		cancel()
		if err != nil {
			t.Fatalf("batch %d: %v", batch+1, err)
		}
		if result.Reset || result.TargetUID != 65 || result.HasMore != (batch < 2) {
			t.Fatalf("batch %d boundary = reset:%v target:%d more:%v", batch+1, result.Reset, result.TargetUID, result.HasMore)
		}
		if len(result.ArchivedMessages) != wantCount {
			t.Fatalf("batch %d archived %d messages, want %d", batch+1, len(result.ArchivedMessages), wantCount)
		}
		for _, archived := range result.ArchivedMessages {
			if archived.UID != wantUID || archived.UIDValidity != state.UIDValidity || archived.AccountID != fixture.account.ID {
				t.Fatalf("batch %d archived message = UID %d generation %d account %d, want UID %d generation %d account %d", batch+1, archived.UID, archived.UIDValidity, archived.AccountID, wantUID, state.UIDValidity, fixture.account.ID)
			}
			if archived.UpstreamSeen != (wantUID%2 == 0) || !reflect.DeepEqual(archived.AliasIDs, []int64{fixture.alias.ID}) {
				t.Fatalf("UID %d lost its flags or recipient mapping: %#v", wantUID, archived)
			}
			raw, err := os.ReadFile(archived.RawMIMEPath)
			if err != nil || !bytes.Equal(raw, []byte(fixture.raw[wantUID])) {
				t.Fatalf("UID %d staged MIME mismatch: %v", wantUID, err)
			}
			digest := sha256.Sum256(raw)
			if archived.ContentState != domain.ArchiveContentAvailable || archived.RawSize != int64(len(raw)) || archived.RawSHA256 != hex.EncodeToString(digest[:]) {
				t.Fatalf("UID %d staged MIME metadata mismatch", wantUID)
			}
			wantUID++
		}
		if result.State.LastUID != wantUID-1 || result.State.AccountID != state.AccountID || result.State.UIDValidity != state.UIDValidity {
			t.Fatalf("batch %d cursor = %#v, want committed UID %d", batch+1, result.State, wantUID-1)
		}
		state = result.State
	}
	if wantUID != 66 {
		t.Fatalf("next expected UID = %d, want 66", wantUID)
	}
	status, err := fixture.user.Status("INBOX", &imap.StatusOptions{NumMessages: true, NumUnseen: true})
	if err != nil || status.NumMessages == nil || *status.NumMessages != 65 || status.NumUnseen == nil || *status.NumUnseen != 33 {
		t.Fatalf("upstream flags changed after read-only batches: %#v, %v", status, err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.selections != 3 || fixture.headerFetches != 3 || len(fixture.bodyUIDs) != 65 {
		t.Fatalf("IMAP trace = selections:%d headers:%d full bodies:%d", fixture.selections, fixture.headerFetches, len(fixture.bodyUIDs))
	}
	for index, uid := range fixture.bodyUIDs {
		if uid != imap.UID(index+1) {
			t.Fatalf("body request %d fetched UID %d, want %d", index, uid, index+1)
		}
	}
}

func TestArchiveBodyFetchContextTerminationPreservesCursorAndCleansStaging(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancellation"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			fixture := startArchiveIMAPFixture(t, 2, 2)
			fetcher := NewFetcher()
			fetcher.ArchiveTempDir = t.TempDir()
			previous := fixture.initialState(t)
			previous.UpdatedAt = time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
			ctx, cancel := context.WithCancel(context.Background())
			wantError := context.Canceled
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
				wantError = context.DeadlineExceeded
			}
			defer cancel()
			type outcome struct {
				result domain.MailboxSyncResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := fetcher.FetchIncremental(ctx, fixture.account, "fixture-password", []domain.Alias{fixture.alias}, &previous, nil)
				done <- outcome{result: result, err: err}
			}()
			select {
			case <-fixture.bodyStarted:
			case got := <-done:
				t.Fatalf("fetch returned before the blocked body request: %v", got.err)
			case <-time.After(3 * time.Second):
				t.Fatal("full BODY.PEEK request did not reach the fixture")
			}
			// One complete earlier MIME plus the in-progress literal must both
			// exist before cancellation, so cleanup cannot pass vacuously.
			stagingDeadline := time.Now().Add(time.Second)
			for {
				entries, err := os.ReadDir(fetcher.ArchiveTempDir)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) == 2 {
					break
				}
				if time.Now().After(stagingDeadline) {
					t.Fatalf("staged MIME files before termination = %d, want complete and partial files", len(entries))
				}
				time.Sleep(5 * time.Millisecond)
			}
			if !deadline {
				cancel()
			}
			select {
			case got := <-done:
				if !errors.Is(got.err, wantError) {
					t.Fatalf("body fetch error = %v, want underlying %v instead of transport EOF", got.err, wantError)
				}
				if !reflect.DeepEqual(got.result.State, previous) || len(got.result.ArchivedMessages) != 0 {
					t.Fatalf("failed batch advanced cursor or published staged messages: %#v", got.result)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("context termination did not interrupt blocked BODY.PEEK")
			}
			entries, err := os.ReadDir(fetcher.ArchiveTempDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("staging after failed batch = %v, %v; want empty", entries, err)
			}
		})
	}
}

type archiveIMAPFixture struct {
	account domain.Account
	alias   domain.Alias
	user    *imapmemserver.User
	raw     map[uint32]string

	mu            sync.Mutex
	selections    int
	loginCount    int
	headerFetches int
	bodyUIDs      []imap.UID
	blockUID      imap.UID
	bodyStarted   chan struct{}
	releaseBody   chan struct{}
	searchPolicy  func(*imap.SearchCriteria) error
}

func startArchiveIMAPFixture(t *testing.T, messageCount int, blockUID imap.UID) *archiveIMAPFixture {
	t.Helper()
	fixture := &archiveIMAPFixture{
		account:  domain.Account{ID: 17, MailboxType: domain.MailboxTypeCustom, Email: "primary@example.test", IMAPHost: "127.0.0.1", IMAPUsername: "primary@example.test", Enabled: true},
		alias:    domain.Alias{ID: 29, AccountID: 17, Address: "alias@example.test", Enabled: true},
		raw:      make(map[uint32]string, messageCount),
		blockUID: blockUID, bodyStarted: make(chan struct{}), releaseBody: make(chan struct{}),
	}
	fixture.user = imapmemserver.NewUser(fixture.account.IMAPUsername, "fixture-password")
	if err := fixture.user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	for uid := 1; uid <= messageCount; uid++ {
		raw := fmt.Sprintf("From: sender@example.test\r\nX-Original-To: alias@example.test\r\nTo: alias@example.test\r\nSubject: message %d\r\nMessage-ID: <%d@example.test>\r\nDate: Fri, 11 Sep 2026 01:00:00 +0000\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nVerification code %06d\r\n", uid, uid, 100000+uid)
		fixture.raw[uint32(uid)] = raw
		options := &imap.AppendOptions{Time: time.Date(2026, 9, 11, 1, 0, uid, 0, time.UTC)}
		if uid%2 == 0 {
			options.Flags = []imap.Flag{imap.FlagSeen}
		}
		if _, err := fixture.user.Append("INBOX", strings.NewReader(raw), options); err != nil {
			t.Fatal(err)
		}
	}
	backend := imapmemserver.New()
	backend.AddUser(fixture.user)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{archiveFixtureTLSCertificate}, MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.account.IMAPPort = listener.Addr().(*net.TCPAddr).Port
	server := imapserver.New(&imapserver.Options{
		Logger: log.New(io.Discard, "", 0),
		NewSession: func(_ *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return &archiveIMAPSession{Session: backend.NewSession(), fixture: fixture}, nil, nil
		},
	})
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		close(fixture.releaseBody)
		_ = listener.Close()
		_ = server.Close()
		select {
		case err := <-serverDone:
			if err != nil && !strings.Contains(err.Error(), "server closed") {
				t.Errorf("stop IMAP fixture: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("IMAP fixture did not stop")
		}
	})
	return fixture
}

func (fixture *archiveIMAPFixture) initialState(t *testing.T) domain.IMAPSyncState {
	t.Helper()
	status, err := fixture.user.Status("INBOX", &imap.StatusOptions{UIDValidity: true})
	if err != nil {
		t.Fatal(err)
	}
	return domain.IMAPSyncState{AccountID: fixture.account.ID, UIDValidity: status.UIDValidity}
}

type archiveIMAPSession struct {
	imapserver.Session
	fixture *archiveIMAPFixture
}

func (session *archiveIMAPSession) Login(username, password string) error {
	session.fixture.mu.Lock()
	session.fixture.loginCount++
	session.fixture.mu.Unlock()
	return session.Session.Login(username, password)
}

func (session *archiveIMAPSession) Search(kind imapserver.NumKind, criteria *imap.SearchCriteria, options *imap.SearchOptions) (*imap.SearchData, error) {
	session.fixture.mu.Lock()
	policy := session.fixture.searchPolicy
	session.fixture.mu.Unlock()
	if policy != nil {
		if err := policy(criteria); err != nil {
			return nil, err
		}
	}
	return session.Session.Search(kind, criteria, options)
}

func (session *archiveIMAPSession) Select(mailbox string, options *imap.SelectOptions) (*imap.SelectData, error) {
	if options == nil || !options.ReadOnly {
		return nil, errors.New("archive fixture requires EXAMINE, not writable SELECT")
	}
	session.fixture.mu.Lock()
	session.fixture.selections++
	session.fixture.mu.Unlock()
	return session.Session.Select(mailbox, options)
}

func (session *archiveIMAPSession) Store(_ *imapserver.FetchWriter, _ imap.NumSet, _ *imap.StoreFlags, _ *imap.StoreOptions) error {
	return errors.New("archive fixture rejects upstream flag changes")
}

func (session *archiveIMAPSession) Fetch(w *imapserver.FetchWriter, numSet imap.NumSet, options *imap.FetchOptions) error {
	for _, section := range options.BodySection {
		if !section.Peek {
			return errors.New("archive fixture requires BODY.PEEK for all content reads")
		}
		if section.Specifier != imap.PartSpecifierNone {
			session.fixture.mu.Lock()
			session.fixture.headerFetches++
			session.fixture.mu.Unlock()
			continue
		}
		set, ok := numSet.(imap.UIDSet)
		if !ok {
			return errors.New("archive fixture requires UID FETCH for full messages")
		}
		uids, ok := set.Nums()
		if !ok || len(uids) != 1 {
			return fmt.Errorf("full body request = %v, want one static UID", numSet)
		}
		uid := uids[0]
		session.fixture.mu.Lock()
		session.fixture.bodyUIDs = append(session.fixture.bodyUIDs, uid)
		session.fixture.mu.Unlock()
		if uid == session.fixture.blockUID {
			response := w.CreateMessage(uint32(uid))
			defer response.Close()
			response.WriteUID(uid)
			literal := response.WriteBodySection(section, 64<<10)
			defer literal.Close()
			// Exceed the server's buffer so the client starts staging a literal
			// that cannot finish until its own context closes the connection.
			if _, err := literal.Write(bytes.Repeat([]byte("x"), 8<<10)); err != nil {
				return err
			}
			close(session.fixture.bodyStarted)
			<-session.fixture.releaseBody
			return errors.New("fixture ended its deliberately unfinished literal")
		}
	}
	return session.Session.Fetch(w, numSet, options)
}
