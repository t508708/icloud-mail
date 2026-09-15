package apple

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type webMailFixture struct {
	calls        []string
	bodies       []map[string]any
	unauthorized bool
	match        bool
}

func (f *webMailFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	var obj map[string]any
	_ = json.Unmarshal(b, &obj)
	f.calls = append(f.calls, r.URL.Path)
	f.bodies = append(f.bodies, obj)
	if f.unauthorized {
		return testResponse(r, http.StatusUnauthorized, `{"error":"expired"}`, nil), nil
	}
	var body string
	switch r.URL.Path {
	case "/mailws2/v1/geqs/query":
		body = `{"domainObjects":[{"identifier":"inbox-1","name":"INBOX","uidValidity":77}]}`
	case "/mailws2/v1/thread/search":
		body = `{"threadList":[{"threadId":"thread-1"}]}`
	case "/mailws2/v1/thread/get":
		if !f.match {
			body = `{"messageMetadataList":[{"uid":9,"folder":"INBOX","to":[{"email":"other@icloud.com","name":"alias@icloud.com"}],"parts":[{"partId":"p","contentType":"text/plain"}]}]}`
		} else {
			body = `{"messageMetadataList":[{"uid":9,"folder":"INBOX","to":[{"email":"alias@icloud.com","name":"Other"}],"parts":[{"partId":"p","contentType":"text/plain"}]}]}`
		}
	case "/mailws2/v1/message/get":
		body = `{"longHeader":"From: sender@example.com","parts":[{"content":"body"}]}`
	default:
		body = `{}`
	}
	return testResponse(r, http.StatusOK, body, nil), nil
}

func webMailTestClient(t *testing.T, f *webMailFixture) *Client {
	c, err := NewClient(Config{Transport: f, MaxResponseBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func webMailTestSession() Session {
	return Session{Region: RegionGlobal, DSID: "42", ClientID: "client-1", MailGatewayURL: "https://p01-mccgateway.icloud.com", Cookies: []PersistentCookie{{Name: "X-APPLE-WEBAUTH-USER", Value: "u", Domain: "icloud.com", Path: "/"}}}
}

func TestReadAliasMailUsesReadOnlyProtocolAndExactRecipient(t *testing.T) {
	f := &webMailFixture{match: true}
	result, _, err := webMailTestClient(t, f).ReadAliasMail(context.Background(), webMailTestSession(), "alias@icloud.com")
	if err != nil {
		t.Fatal(err)
	}
	if result.UIDValidity != 77 || len(result.Messages) != 1 || result.Messages[0].TextBody != "body" {
		t.Fatalf("result=%+v", result)
	}
	for _, p := range f.calls {
		if p != "/mailws2/v1/geqs/query" && p != "/mailws2/v1/thread/search" && p != "/mailws2/v1/thread/get" && p != "/mailws2/v1/message/get" {
			t.Fatalf("unexpected endpoint %q", p)
		}
	}
	for i, b := range f.bodies {
		if f.calls[i] == "/mailws2/v1/message/get" && b["dontMarkAsRead"] != true {
			t.Fatalf("message request=%v", b)
		}
	}
	if len(f.bodies[0]) == 0 {
		t.Fatal("request body was not a JSON object")
	}
}

func TestReadAliasMailDoesNotFetchBodyForNameOrSimilarAddress(t *testing.T) {
	f := &webMailFixture{}
	result, _, err := webMailTestClient(t, f).ReadAliasMail(context.Background(), webMailTestSession(), "alias@icloud.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 0 {
		t.Fatalf("result=%+v", result)
	}
	for _, p := range f.calls {
		if strings.HasSuffix(p, "/message/get") {
			t.Fatal("body fetched for non-matching recipient")
		}
	}
}

func TestReadAliasMailAuthenticationErrorIsNotEmptySuccess(t *testing.T) {
	f := &webMailFixture{unauthorized: true}
	result, _, err := webMailTestClient(t, f).ReadAliasMail(context.Background(), webMailTestSession(), "alias@icloud.com")
	if err == nil || len(result.Messages) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
