package apple

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAccountCallCapturesTrustTokenAndRoundTripsIt(t *testing.T) {
	client, err := NewClient(Config{Transport: accountTrustTransport(func(r *http.Request) *http.Response {
		response := &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: r}
		response.Header.Set("X-Apple-TwoSV-Trust-Token", "account-trust-token")
		return response
	})})
	if err != nil {
		t.Fatal(err)
	}
	session := AccountSession{AppleID: "owner@example.com"}
	if _, err := client.accountCall(context.Background(), &session, http.MethodGet, accountAuth+"/2sv/trust", nil, accountHeaders(session, true), false); err != nil {
		t.Fatal(err)
	}
	if session.TrustToken != "account-trust-token" {
		t.Fatalf("trust token = %q", session.TrustToken)
	}
	encoded, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	var decoded AccountSession
	if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.TrustToken != session.TrustToken {
		t.Fatalf("trust token did not round-trip: %q %v", decoded.TrustToken, err)
	}
}

type accountTrustTransport func(*http.Request) *http.Response

func (f accountTrustTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r), nil
}

func TestAccountTrustFailureUsesFixedOperation(t *testing.T) {
	calls := 0
	client, err := NewClient(Config{Transport: accountTrustTransport(func(r *http.Request) *http.Response {
		calls++
		status := http.StatusNoContent
		body := ""
		if calls == 2 {
			status = http.StatusUnauthorized
			body = `{"errorMessage":"secret raw response"}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}
	})})
	if err != nil {
		t.Fatal(err)
	}
	session, trustErr := client.VerifyAccountCode(context.Background(), AccountSession{AppleID: "owner@example.com", SCNT: "scnt", FrameID: "frame"}, "123456")
	if trustErr == nil || !strings.Contains(trustErr.Error(), "trust Apple Account session") || strings.Contains(trustErr.Error(), "secret raw response") || session.AppleID == "" {
		t.Fatalf("trust failure = %v, session = %#v", trustErr, session)
	}
}
