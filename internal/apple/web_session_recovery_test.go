package apple

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func recoveryAccountBody(dsid, country string) string {
	return `{"dsInfo":{"dsid":"` + dsid + `","primaryEmail":"owner@example.com","countryCode":"` + country + `","hsaVersion":2},"hsaTrustedBrowser":true,"webservices":{"premiummailsettings":{"url":"https://p01-maildomainws.icloud.com"}}}`
}

func TestValidate421RecoversWithTrustedCheckpoint(t *testing.T) {
	var paths []string
	client, err := NewClient(Config{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		paths = append(paths, r.URL.Path)
		if r.URL.Path == "/setup/ws/1/validate" {
			return testResponse(r, http.StatusMisdirectedRequest, `{"country":"CN","dsInfo":{"dsid":"attacker"}}`, http.Header{"Set-Cookie": {"scnt=attacker; Path=/"}}), nil
		}
		if r.URL.Path == "/setup/ws/1/accountLogin" {
			if got := r.Header.Get("X-Apple-Session-Token"); got != "" {
				t.Fatalf("token sent as header: %q", got)
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["dsWebAuthToken"] != "trusted-token" || payload["trustToken"] != "trusted-trust" || payload["extended_login"] != true {
				t.Fatal("recovery omitted trusted token exchange fields")
			}
			if strings.Contains(r.Header.Get("Cookie"), "attacker") || !strings.Contains(r.Header.Get("Cookie"), "base=trusted") {
				t.Fatal("recovery cookie checkpoint was corrupted")
			}
			return testResponse(r, http.StatusOK, recoveryAccountBody("42", "US"), http.Header{"Set-Cookie": {"rotated=ok; Path=/"}}), nil
		}
		return nil, errors.New("unexpected request")
	})})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Validate(context.Background(), Session{Region: RegionGlobal, CountryCode: "US", SessionID: "sid", SessionToken: "trusted-token", TrustToken: "trusted-trust", SCNT: "trusted-scnt", DSID: "42", Cookies: []PersistentCookie{{Name: "base", Value: "trusted", Domain: "setup.icloud.com", Path: "/"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.DSID != "42" || got.Region != RegionGlobal || got.SessionToken != "trusted-token" || got.TrustToken != "trusted-trust" || len(got.Cookies) != 2 {
		t.Fatalf("recovery result = %#v", got)
	}
	if len(paths) != 2 {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestValidateDoesNotRecoverOtherFailures(t *testing.T) {
	for _, status := range []int{401, 403, 450, 429, 500, 503, 0} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			client, err := NewClient(Config{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != "/setup/ws/1/validate" {
					t.Fatal("unexpected recovery request")
				}
				if status == 0 {
					return nil, errors.New("fixture transport failure")
				}
				return testResponse(r, status, `{}`, nil), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Validate(context.Background(), Session{Region: RegionGlobal, SessionToken: "trusted-token"})
			if err == nil || calls != 1 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
			wantExpired := status == 401 || status == 403 || status == 450
			if errors.Is(err, ErrInvalidSession) != wantExpired {
				t.Fatalf("classification: %v", err)
			}
		})
	}
}

func TestValidateRecoveryFailurePreservesCheckpointAndStops(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		kind   error
	}{
		{"throttled", 429, `{}`, ErrService},
		{"misdirected", 421, `{}`, ErrService},
		{"server error", 503, `{}`, ErrService},
		{"transport error", 0, "", ErrService},
		{"expired", 401, `{}`, ErrInvalidSession},
		{"wrong identity", 200, recoveryAccountBody("43", "US"), ErrInvalidResponse},
		{"missing identity", 200, recoveryAccountBody("", "US"), ErrInvalidResponse},
		{"needs verification", 200, `{"dsInfo":{"dsid":"42","hsaVersion":2},"hsaChallengeRequired":true}`, ErrInvalidSession},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client, err := NewClient(Config{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 && r.URL.Path == "/setup/ws/1/validate" {
					return testResponse(r, 421, `{}`, nil), nil
				}
				if calls != 2 || r.URL.Path != "/setup/ws/1/accountLogin" {
					t.Fatal("recovery was replayed or used another endpoint")
				}
				if test.status == 0 {
					return nil, errors.New("fixture transport failure")
				}
				return testResponse(r, test.status, test.body, http.Header{"X-Apple-Session-Token": {"untrusted"}, "Set-Cookie": {"base=untrusted; Path=/"}}), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			original := Session{Region: RegionGlobal, DSID: "42", SessionToken: "trusted-token", TrustToken: "trusted-trust", Cookies: []PersistentCookie{{Name: "base", Value: "trusted", Domain: "setup.icloud.com", Path: "/"}}}
			got, err := client.Validate(context.Background(), original)
			if !errors.Is(err, test.kind) || calls != 2 {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
			if !reflect.DeepEqual(got, original) {
				t.Fatal("failed recovery changed trusted checkpoint")
			}
		})
	}
}

func TestValidateRecoveryRoutesToChinaAtMostOnce(t *testing.T) {
	for _, region := range []Region{RegionGlobal, RegionChina} {
		t.Run(string(region), func(t *testing.T) {
			var paths []string
			client, err := NewClient(Config{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				paths = append(paths, r.URL.Host+r.URL.Path)
				if len(paths) > 3 {
					t.Fatal("region routing loop")
				}
				if r.URL.Path == "/setup/ws/1/validate" {
					return testResponse(r, 421, `{"requestInfo":[{"country":"CN"}]}`, nil), nil
				}
				if r.URL.Path != "/setup/ws/1/accountLogin" || r.URL.Host != "setup.icloud.com.cn" {
					t.Fatal("recovery used wrong region or endpoint")
				}
				return testResponse(r, 200, recoveryAccountBody("42", "CN"), nil), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			got, err := client.Validate(context.Background(), Session{Region: region, DSID: "42", SessionToken: "trusted-token"})
			want := 2
			if region == RegionGlobal {
				want = 3
			}
			if err != nil || got.Region != RegionChina || len(paths) != want {
				t.Fatalf("error=%v region=%s paths=%v", err, got.Region, paths)
			}
		})
	}
}

func TestValidate421ThrottleDoesNotExchangeToken(t *testing.T) {
	calls := 0
	client, err := NewClient(Config{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return testResponse(r, 421, `{"errorCode":"-41015"}`, nil), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Validate(context.Background(), Session{Region: RegionGlobal, SessionToken: "trusted-token"})
	if !IsRateLimited(err) || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestValidate421WithoutTokenDoesNotRecover(t *testing.T) {
	calls := 0
	client, err := NewClient(Config{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return testResponse(r, http.StatusMisdirectedRequest, `{"country":"US"}`, nil), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Validate(context.Background(), Session{Region: RegionGlobal})
	if err == nil || !errors.Is(err, ErrService) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
