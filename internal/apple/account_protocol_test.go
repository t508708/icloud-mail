package apple

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type accountTestRT func(*http.Request) (*http.Response, error)

func (f accountTestRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func accountResp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: &http.Request{}}
}
func accountClient(t *testing.T, f accountTestRT) *Client {
	t.Helper()
	c, e := NewClient(Config{Transport: f})
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func TestAccountAliasSingleCompleteAndPayload(t *testing.T) {
	n := 0
	c := accountClient(t, func(r *http.Request) (*http.Response, error) {
		n++
		if r.URL.Host != "appleid.apple.com" {
			t.Fatalf("host %s", r.URL.Host)
		}
		if r.Method == http.MethodPost {
			return accountResp(200, `{"emailAddress":"abc@icloud.com"}`), nil
		}
		if r.Method == http.MethodPut {
			return accountResp(200, `{"emailAddress":"abc@icloud.com","id":"id1","active":true}`), nil
		}
		return accountResp(500, `{}`), nil
	})
	s := AccountSession{SCNT: "s", APIKey: "k", ExpiresAt: time.Now().Add(time.Hour)}
	a, _, e := c.CreateAccountAlias(context.Background(), s, "L", "N")
	if e != nil || a.HME != "abc@icloud.com" || a.AnonymousID != "id1" || n != 2 {
		t.Fatalf("alias=%+v n=%d err=%v", a, n, e)
	}
}

func TestAccountAliasAmbiguousKeepsCandidate(t *testing.T) {
	n := 0
	c := accountClient(t, func(r *http.Request) (*http.Response, error) {
		n++
		if r.Method == http.MethodPost {
			return accountResp(200, `{"emailAddress":"abc@icloud.com"}`), nil
		}
		return nil, errors.New("disconnect")
	})
	a, _, e := c.CreateAccountAlias(context.Background(), AccountSession{SCNT: "s", APIKey: "k", ExpiresAt: time.Now().Add(time.Hour)}, "L", "")
	if e == nil || a.HME != "abc@icloud.com" || n != 2 {
		t.Fatalf("a=%+v n=%d e=%v", a, n, e)
	}
}

func TestAccountVerifyCodeShapeAndValidation(t *testing.T) {
	c := accountClient(t, func(r *http.Request) (*http.Response, error) { return accountResp(200, `{"timeOutInterval":30}`), nil })
	_, e := c.VerifyAccountCode(context.Background(), AccountSession{SCNT: "s", FrameID: "f"}, "12")
	if !errors.Is(e, ErrTwoFactorCode) {
		t.Fatal(e)
	}
}

func TestAccountHashcashDeterministicShape(t *testing.T) {
	got, e := accountHashcash(context.Background(), 1, "x")
	if e != nil || got == "" || !strings.HasPrefix(got, "1:") {
		t.Fatalf("%q %v", got, e)
	}
	d := sha1.Sum([]byte(got))
	if d[0]&0x80 != 0 {
		t.Fatalf("proof lacks requested leading bit")
	}
}

func TestAccountAliasRateAndAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
		keep bool
	}{{429, `{"errorCode":"-41015"}`, false}, {500, `{}`, true}} {
		n := 0
		c := accountClient(t, func(r *http.Request) (*http.Response, error) {
			n++
			if n == 1 {
				return accountResp(200, `{"emailAddress":"a@icloud.com"}`), nil
			}
			return accountResp(tc.code, tc.body), nil
		})
		a, _, e := c.CreateAccountAlias(context.Background(), AccountSession{SCNT: "s", APIKey: "k", ExpiresAt: time.Now().Add(time.Hour)}, "L", "")
		if e == nil || n != 2 || (tc.keep != (a.HME != "")) {
			t.Fatalf("code %d alias=%+v calls=%d err=%v", tc.code, a, n, e)
		}
	}
}

func TestAccountFailureDoesNotMutateState(t *testing.T) {
	c := accountClient(t, func(r *http.Request) (*http.Response, error) {
		resp := accountResp(500, `{}`)
		resp.Header.Set("SCNT", "new")
		resp.Header.Add("Set-Cookie", "x=y; Path=/")
		return resp, nil
	})
	s := AccountSession{SCNT: "old", APIKey: "k", Cookies: []PersistentCookie{{Name: "a", Value: "b", Domain: "appleid.apple.com", Path: "/"}}, ExpiresAt: time.Now().Add(time.Hour)}
	_, _, e := c.CreateAccountAlias(context.Background(), s, "L", "")
	if e == nil || s.SCNT != "old" || len(s.Cookies) != 1 {
		t.Fatalf("state mutated: %+v err=%v", s, e)
	}
}

func TestAccountCallRejectsForeignHost(t *testing.T) {
	called := false
	c := accountClient(t, func(r *http.Request) (*http.Response, error) { called = true; return accountResp(200, `{}`), nil })
	_, err := c.accountCall(context.Background(), &AccountSession{}, http.MethodGet, "https://example.invalid/x", nil, http.Header{}, false)
	if called || !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

func TestAccountAliasGenerateRateReturnsEmpty(t *testing.T) {
	n := 0
	c := accountClient(t, func(r *http.Request) (*http.Response, error) {
		n++
		return accountResp(429, `{"errorCode":"-41015"}`), nil
	})
	a, _, err := c.CreateAccountAlias(context.Background(), AccountSession{SCNT: "s", APIKey: "k", ExpiresAt: time.Now().Add(time.Hour)}, "L", "")
	if n != 1 || a.HME != "" || err == nil || !IsRateLimited(err) {
		t.Fatalf("alias=%+v calls=%d err=%v", a, n, err)
	}
}

func TestAccountVerifyCodePayloadAndRefresh(t *testing.T) {
	seen := false
	c := accountClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/appleauth/auth/verify/trusteddevice/securitycode":
			var v map[string]map[string]string
			if json.NewDecoder(r.Body).Decode(&v) != nil || v["securityCode"]["code"] != "123456" {
				t.Fatalf("bad security payload")
			}
			seen = true
			return accountResp(204, ""), nil
		case "/appleauth/auth/2sv/trust":
			return accountResp(204, ""), nil
		case "/account/manage/gs/ws/token":
			return accountResp(200, `{"timeOutInterval":30}`), nil
		case "/account/manage":
			return accountResp(200, `{"apiKey":"k2"}`), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
			return nil, nil
		}
	})
	s, err := c.VerifyAccountCode(context.Background(), AccountSession{SCNT: "s", FrameID: "frame"}, "123456")
	if err != nil || !seen || s.APIKey != "k2" || s.AuthenticatedAt.IsZero() {
		t.Fatalf("session=%+v err=%v", s, err)
	}
}

func TestAccountSignInSRPChallenge(t *testing.T) {
	paths := map[string]int{}
	c := accountClient(t, func(r *http.Request) (*http.Response, error) {
		paths[r.URL.Path]++
		switch r.URL.Path {
		case "/account/manage/section/privacy":
			if r.Header.Get("Content-Type") != "" {
				t.Fatal("document request marked as JSON")
			}
			return accountResp(200, `<html/>`), nil
		case "/bootstrap/portal":
			return accountResp(200, `{"timeOutInterval":30}`), nil
		case "/account/manage/gs/ws/token":
			return accountResp(200, `{}`), nil
		case "/appleauth/auth/authorize/signin":
			if r.Header.Get("Content-Type") != "" {
				t.Fatal("iframe request marked as JSON")
			}
			r := accountResp(200, `{}`)
			r.Header.Set("X-Apple-HC-Bits", "1")
			r.Header.Set("X-Apple-HC-Challenge", "x")
			return r, nil
		case "/appleauth/auth/verify/device/key/challenge", "/appleauth/auth/federate":
			return accountResp(200, `{}`), nil
		case "/appleauth/auth/signin/init":
			return &http.Response{StatusCode: 200, Header: http.Header{"X-Apple-HC-Bits": []string{"1"}, "X-Apple-HC-Challenge": []string{"x"}}, Body: io.NopCloser(strings.NewReader(`{"salt":"c2FsdFNhbHQ=","b":"Ag==","c":"Yw==","iteration":1000,"protocol":"s2k_fo"}`))}, nil
		case "/appleauth/auth/signin/complete":
			if r.Header.Get("X-Apple-Widget-Key") != accountWidget {
				t.Fatal("missing widget key")
			}
			var v map[string]any
			_ = json.NewDecoder(r.Body).Decode(&v)
			if v["m1"] == nil || v["m2"] == nil || v["password"] != nil {
				t.Fatalf("bad signin payload: %#v", v)
			}
			return accountResp(409, `{}`), nil
		case "/appleauth/auth":
			return accountResp(200, `{}`), nil
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			return accountResp(404, `{}`), nil
		}
	})
	previous := &AccountSession{AppleID: "other@example.com", Cookies: []PersistentCookie{{Name: "old", Value: "x", Domain: "appleid.apple.com", Path: "/"}}}
	s, pending, err := c.SignInAccount(context.Background(), "user@example.com", "pass", RegionGlobal, previous)
	if err != nil || !pending || s.AppleID != "user@example.com" || len(s.Cookies) != 0 {
		t.Fatalf("session=%+v pending=%v err=%v", s, pending, err)
	}
}
