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

func TestDirectoryAuthenticationFailureKeepsTrustedCheckpointAndPresenceDiagnostics(t *testing.T) {
	for _, status := range []int{401, 403, 450} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			requests := 0
			client, err := NewClient(Config{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if request.URL.Path != "/v2/hme/list" || request.Method != http.MethodGet {
					t.Fatal("directory failure triggered authentication or a write")
				}
				return testResponse(request, status, `{"message":"fixture-secret-body"}`, http.Header{
					"X-Apple-Session-Token": {"fixture-untrusted-token"},
					"Set-Cookie":            {"X-APPLE-WEBAUTH-TOKEN=expired; Max-Age=-1; Domain=.icloud.com.cn; Path=/"},
				}), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			original := Session{Region: RegionChina, DSID: "fixture-dsid", ClientID: "fixture-client", AppleID: "owner@example.test", SessionToken: "fixture-trusted-token", PremiumMailSettingsURL: "https://p01-maildomainws.icloud.com.cn", HideMyEmailActive: true, Cookies: []PersistentCookie{
				{Name: "X-APPLE-WEBAUTH-TOKEN", Value: "fixture-auth-cookie", Domain: "icloud.com.cn", Path: "/"},
				{Name: "X-APPLE-WEBAUTH-USER", Value: "fixture-user-cookie", Domain: "icloud.com.cn", Path: "/"},
				{Name: "other-region", Value: "fixture-global-cookie", Domain: "icloud.com", Path: "/"},
			}}
			_, got, err := client.ListAliases(context.Background(), original)
			if !errors.Is(err, ErrHMEAuthentication) || errors.Is(err, ErrInvalidSession) || requests != 1 {
				t.Fatalf("error=%v requests=%d", err, requests)
			}
			if !reflect.DeepEqual(got, original) {
				t.Fatal("failed directory response changed trusted session")
			}
			var typed *Error
			if !errors.As(err, &typed) || typed.WebSession == nil {
				t.Fatal("missing directory diagnostics")
			}
			diag := typed.WebSession
			if diag.Region != RegionChina || diag.ServiceRegion != RegionChina || diag.MatchingCookies != 2 || !diag.WebAuthPresent || !diag.WebUserPresent || !diag.HMEActive || diag.HMEAvailable {
				t.Fatalf("diagnostics=%+v", diag)
			}
			encoded, err := json.Marshal(typed)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "fixture-") || strings.Contains(string(encoded), "example.test") || strings.Contains(string(encoded), "maildomainws") {
				t.Fatal("diagnostic contains a secret or account/endpoint identifier")
			}
		})
	}
}

func TestDirectoryDiagnosticsDetectRegionCookieMismatch(t *testing.T) {
	client, err := NewClient(Config{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) { return testResponse(request, 401, `{}`, nil), nil })})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.ListAliases(context.Background(), Session{Region: RegionChina, DSID: "42", ClientID: "fixture", PremiumMailSettingsURL: "https://p01-maildomainws.icloud.com", Cookies: []PersistentCookie{{Name: "X-APPLE-WEBAUTH-TOKEN", Value: "fixture", Domain: "icloud.com.cn", Path: "/"}}})
	var typed *Error
	if !errors.As(err, &typed) || typed.WebSession == nil || typed.WebSession.MatchingCookies != 0 || typed.WebSession.WebAuthPresent || typed.WebSession.Region == typed.WebSession.ServiceRegion {
		t.Fatal("cookie/region mismatch was not diagnosed")
	}
}
