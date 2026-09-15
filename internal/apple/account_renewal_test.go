package apple

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestAccountCreationRefreshesBeforeIdleDeadline(t *testing.T) {
	var calls []string
	c := accountClient(t, func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch len(calls) {
		case 1:
			if r.URL.Path != "/account/manage/gs/ws/token" || r.Header.Get("X-Apple-Api-Key") != "" {
				t.Fatal("refresh must first request the management token without the old API key")
			}
			response := accountResp(200, `{"timeOutInterval":15}`)
			response.Header.Set("scnt", "renewed-scnt")
			response.Header.Add("Set-Cookie", "session=renewed; Path=/; Secure; HttpOnly")
			return response, nil
		case 2:
			cookie, err := r.Cookie("session")
			if r.URL.Path != "/account/manage" || r.Header.Get("scnt") != "renewed-scnt" || err != nil || cookie.Value != "renewed" {
				t.Fatal("refresh lost rotated state")
			}
			return accountResp(200, `{"apiKey":"renewed-key"}`), nil
		case 3:
			if r.URL.Path != "/account/manage/forwardemail" || r.Header.Get("X-Apple-Api-Key") != "renewed-key" {
				t.Fatal("refresh did not touch the management resource")
			}
			return accountResp(200, `{}`), nil
		case 4:
			if r.Method != http.MethodPost || r.Header.Get("X-Apple-Api-Key") != "renewed-key" {
				t.Fatal("creation did not use the renewed API key")
			}
			return accountResp(200, `{"emailAddress":"renewed@icloud.com"}`), nil
		default:
			return accountResp(200, `{"emailAddress":"renewed@icloud.com","id":"one","active":true}`), nil
		}
	})
	now := time.Now()
	alias, session, err := c.CreateAccountAlias(context.Background(), AccountSession{SCNT: "old", APIKey: "old", AuthenticatedAt: now.Add(-14 * time.Minute), ExpiresAt: now.Add(time.Minute)}, "label", "")
	if err != nil || alias.HME != "renewed@icloud.com" || len(calls) != 5 || !session.ExpiresAt.After(now.Add(14*time.Minute)) || session.RefreshedAt.IsZero() || session.UpdatedAt.IsZero() {
		t.Fatalf("renewal flow failed: calls=%v error=%v", calls, err)
	}
}

func TestAccountRefreshSchedulingHonorsTTLAndBackoff(t *testing.T) {
	now := time.Now()
	session := AccountSession{RefreshedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if session.NeedsRefresh(now.Add(11*time.Minute)) || !session.NeedsRefresh(now.Add(12*time.Minute)) {
		t.Fatal("refresh must run before the fifteen-minute deadline")
	}
	session.RefreshAfter = now.Add(time.Hour)
	if session.NeedsRefresh(now.Add(16 * time.Minute)) {
		t.Fatal("ignored persisted backoff")
	}
	session.RefreshRejected = true
	if session.NeedsRefresh(now.Add(2 * time.Hour)) {
		t.Fatal("repeated a rejected session")
	}
}
