package apple

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestAccountRecoveryReacquiresTokenWithoutStaleSCNT(t *testing.T) {
	for _, tc := range []struct {
		name        string
		blankStatus int
		fallback    bool
		wantErr     error
		wantTokens  int
	}{
		{"cookie_recovery", 200, false, nil, 2},
		{"accepted_checkpoint_fallback", 401, true, nil, 3},
		{"rate_limit_stops_recovery", 429, false, ErrService, 2},
		{"expired_cookie_stops_after_one_fallback", 401, false, ErrInvalidSession, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tokens, warmed, resources := 0, false, 0
			c := accountClient(t, func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet {
					t.Fatal("recovery sent a mutation")
				}
				switch r.URL.Path {
				case "/account/manage/gs/ws/token":
					tokens++
					cookie, err := r.Cookie("management")
					if err != nil || cookie.Value != "saved-cookie" {
						t.Fatal("recovery lost the saved cookie")
					}
					if tokens == 1 {
						return accountResp(401, `{}`), nil
					}
					if !warmed || tokens == 2 && r.Header.Get("scnt") != "" {
						t.Fatal("recovery did not bootstrap without stale SCNT")
					}
					if tokens == 2 && tc.blankStatus != 200 {
						return accountResp(tc.blankStatus, `{}`), nil
					}
					if tokens == 3 {
						if r.Header.Get("scnt") != "old" {
							t.Fatal("failed token response overwrote the accepted checkpoint")
						}
						if !tc.fallback {
							return accountResp(401, `{}`), nil
						}
					}
					resp := accountResp(200, `{"timeOutInterval":15}`)
					resp.Header.Set("scnt", "fresh")
					return resp, nil
				case "/account/manage/section/privacy":
					return accountResp(200, "<html></html>"), nil
				case "/bootstrap/portal":
					warmed = true
					return accountResp(200, `{}`), nil
				case "/account/manage":
					if r.Header.Get("scnt") != "fresh" {
						t.Fatal("management read used stale SCNT")
					}
					return accountResp(200, `{"apiKey":"fresh-key"}`), nil
				case "/account/manage/forwardemail":
					resources++
					return accountResp(200, `{}`), nil
				default:
					t.Fatalf("unexpected recovery path %s", r.URL.Path)
					return nil, nil
				}
			})
			login := time.Now().Add(-8 * time.Hour)
			saved := AccountSession{SCNT: "old", APIKey: "old-key", AuthenticatedAt: login,
				Cookies: []PersistentCookie{{Name: "management", Value: "saved-cookie", Domain: "apple.com", Path: "/", Secure: true}}}
			got, err := c.RefreshAccountSession(context.Background(), saved)
			if !errors.Is(err, tc.wantErr) || tokens != tc.wantTokens || !got.AuthenticatedAt.Equal(login) {
				t.Fatalf("tokens=%d err=%v original_login_retained=%v", tokens, err, got.AuthenticatedAt.Equal(login))
			}
			if err == nil && (resources != 1 || got.SCNT != "fresh" || got.APIKey != "fresh-key" || !got.ExpiresAt.After(time.Now())) {
				t.Fatal("recovered session was not validated and renewed")
			}
		})
	}
}

func TestAccountCreationRetriesOnlyExplicitInitialUnauthorized(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			adds, refreshes, completes := 0, 0, 0
			c := accountClient(t, func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/account/manage/email/private/add":
					adds++
					if adds == 1 {
						return accountResp(status, `{}`), nil
					}
					if r.Header.Get("X-Apple-Api-Key") != "fresh-key" {
						t.Fatal("retry used stale credentials")
					}
					return accountResp(200, `{"emailAddress":"recovered@icloud.com"}`), nil
				case "/account/manage/gs/ws/token":
					refreshes++
					return accountResp(200, `{"timeOutInterval":15}`), nil
				case "/account/manage":
					return accountResp(200, `{"apiKey":"fresh-key"}`), nil
				case "/account/manage/forwardemail":
					return accountResp(200, `{}`), nil
				case "/account/manage/email/private/add/complete":
					completes++
					return accountResp(200, `{"id":"alias-id","active":true}`), nil
				default:
					t.Fatalf("unexpected path %s", r.URL.Path)
					return nil, nil
				}
			})
			_, _, err := c.CreateAccountAlias(context.Background(), AccountSession{SCNT: "old", APIKey: "old-key", ExpiresAt: time.Now().Add(time.Hour)}, "label", "note")
			if status == 401 {
				if err != nil || adds != 2 || refreshes != 1 || completes != 1 {
					t.Fatalf("401 recovery: adds=%d refreshes=%d completes=%d err=%v", adds, refreshes, completes, err)
				}
			} else if err == nil || adds != 1 || refreshes != 0 || completes != 0 {
				t.Fatal("non-401 failure was retried")
			}
		})
	}
}
