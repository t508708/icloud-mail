package apple

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// This opt-in check makes only anonymous bootstrap requests, never a sign-in
// or an alias reservation. It must not emit cookies or authentication tokens.
func TestAccountPortalLive(t *testing.T) {
	if os.Getenv("ICLOUD_TEST_ACCOUNT_BOOTSTRAP") != "1" {
		t.Skip("set ICLOUD_TEST_ACCOUNT_BOOTSTRAP=1 for anonymous portal check")
	}
	client, err := NewClient(Config{Transport: accountTestRT(func(r *http.Request) (*http.Response, error) {
		response, err := http.DefaultTransport.RoundTrip(r)
		if response != nil {
			t.Logf("%s %s %s: HTTP %d", r.Method, r.URL.Host, r.URL.Path, response.StatusCode)
		}
		return response, err
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	var session AccountSession
	if err := client.warmAccountPortal(ctx, &session); err != nil {
		t.Fatal(err)
	}
	if session.SCNT == "" || len(session.Cookies) == 0 {
		t.Fatal("bootstrap returned no session state")
	}
}
