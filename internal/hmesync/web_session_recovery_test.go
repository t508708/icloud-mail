package hmesync

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
)

type webRecoveryTransport func(*http.Request) (*http.Response, error)

func (f webRecoveryTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSyncDirectoryRecovers421WithoutPasswordOrClearingGoodSession(t *testing.T) {
	for _, status := range []int{200, 421, 429, 503, 401} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			now := time.Now().UTC()
			repo := newFakeRepository(domain.Account{ID: 3, Email: "primary@icloud.com"}, now)
			calls, lists := 0, 0
			client, err := apple.NewClient(apple.Config{Transport: webRecoveryTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				code, body := 421, `{}`
				header := make(http.Header)
				switch r.URL.Path {
				case "/setup/ws/1/validate":
				case "/setup/ws/1/accountLogin":
					code = status
					if status == 200 {
						body = `{"dsInfo":{"dsid":"42","primaryEmail":"primary@icloud.com","hsaVersion":2},"hsaTrustedBrowser":true,"webservices":{"premiummailsettings":{"url":"https://p01-maildomainws.icloud.com"}}}`
						header.Set("Set-Cookie", "X-APPLE-WEBAUTH-TOKEN=recovered; Domain=.icloud.com; Path=/; Secure")
					}
				case "/v2/hme/list":
					lists++
					if !strings.Contains(r.Header.Get("Cookie"), "X-APPLE-WEBAUTH-TOKEN=recovered") {
						t.Fatal("directory did not use recovered cookie")
					}
					code, body = 200, `{"success":true,"result":{"hmeEmails":[],"forwardToEmails":["primary@icloud.com"],"selectedForwardTo":"primary@icloud.com"}}`
				default:
					t.Fatalf("unexpected password, verification or mutation request: %s", r.URL.Path)
				}
				return &http.Response{StatusCode: code, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			service := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
			storeSession(t, service, repo, 3, apple.Session{AppleID: "owner@example.com", Region: apple.RegionGlobal, DSID: "42", SessionToken: "trusted-token", ClientID: "fixture-client"})
			before := repo.mustSession(t, 3).Ciphertext
			_, err = service.SyncAliases(context.Background(), 3)
			if status == 200 {
				if err != nil || calls != 3 || lists != 1 || repo.imports.Load() != 1 {
					t.Fatalf("error=%v calls=%d lists=%d", err, calls, lists)
				}
				stored, err := service.decryptSession(repo.mustSession(t, 3))
				if err != nil || stored.DSID != "42" || len(stored.Cookies) == 0 {
					t.Fatal("recovered session was not saved")
				}
				return
			}
			if err == nil || calls != 2 || lists != 0 {
				t.Fatalf("error=%v calls=%d lists=%d", err, calls, lists)
			}
			if status == 401 {
				if !errors.Is(err, ErrSessionExpired) || repo.sessionCount() != 0 {
					t.Fatal("explicit token expiry was not surfaced")
				}
			} else if repo.sessionCount() != 1 || repo.mustSession(t, 3).Ciphertext != before || errors.Is(err, ErrSessionExpired) {
				t.Fatal("transient upstream failure erased the saved login")
			}
		})
	}
}
