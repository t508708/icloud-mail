package apple

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const smsOptionsFixture = `<html><script type="application/json" class="boot_args">{"direct":{"twoSV":{"supportedPushModes":["voice","sms"],"phoneNumberVerification":{"hideSendSMSCodeOption":false,"trustedPhoneNumbers":[{"id":7,"pushMode":"sms","nonFTEU":true,"lastTwoDigits":"42","numberWithDialCode":"+1-private-full-number"}]}}}}</script></html>`

func TestWebSMSLoginSendsAndVerifiesPhoneThenTrustsBrowser(t *testing.T) {
	fixture := newAuthFixture(t, http.StatusConflict)
	sends, verifies, devices := 0, 0, 0
	client, err := NewClient(Config{Random: deterministicRandom(), Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/appleauth/auth":
			return testResponse(r, 200, smsOptionsFixture, nil), nil
		case "/appleauth/auth/verify/phone":
			sends++
			var body struct {
				Phone map[string]any `json:"phoneNumber"`
				Mode  string         `json:"mode"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || r.Method != "PUT" || body.Mode != "sms" || body.Phone["id"] != float64(7) || len(body.Phone) != 1 {
				t.Fatal("wrong SMS delivery request")
			}
			return testResponse(r, 200, `{"success":true}`, http.Header{"Scnt": []string{"sms-scnt"}}), nil
		case "/appleauth/auth/verify/phone/securitycode":
			verifies++
			var body struct {
				Phone        map[string]any `json:"phoneNumber"`
				Mode         string         `json:"mode"`
				SecurityCode struct {
					Code string `json:"code"`
				} `json:"securityCode"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || r.Method != "POST" || body.Mode != "sms" || body.Phone["id"] != float64(7) || body.Phone["nonFTEU"] != true || body.SecurityCode.Code != "123456" || r.Header.Get("scnt") != "sms-scnt" {
				t.Fatal("wrong SMS verification request")
			}
			return testResponse(r, 200, `{}`, http.Header{"X-Apple-Session-Token": []string{"session-token"}}), nil
		case "/appleauth/auth/verify/trusteddevice/securitycode":
			devices++
			t.Fatal("SMS login sent a device request")
		}
		return fixture.RoundTrip(r)
	})})
	if err != nil {
		t.Fatal(err)
	}
	session, pending, err := client.SignIn(context.Background(), fixture.appleID, fixture.password, RegionGlobal, nil)
	if err != nil || !pending || session.WebVerification == nil || !session.WebVerification.Sent || session.WebVerification.Method != "sms" || sends != 1 {
		t.Fatalf("pending=%v sends=%d err=%v", pending, sends, err)
	}
	encoded, _ := json.Marshal(session)
	if strings.Contains(string(encoded), "private-full-number") {
		t.Fatal("full phone retained")
	}
	_, err = client.RequestWebVerification(context.Background(), session, "sms", 7)
	if !IsRateLimited(err) || sends != 1 {
		t.Fatal("repeated refresh resent SMS")
	}
	session, err = client.VerifyCode(context.Background(), session, "123456")
	if err != nil || session.DSID == "" || session.WebVerification != nil || verifies != 1 || devices != 0 || fixture.trustRequests != 1 || fixture.accountLoginRequests != 1 {
		t.Fatalf("verify=%d devices=%d trust=%d login=%d err=%v", verifies, devices, fixture.trustRequests, fixture.accountLoginRequests, err)
	}
}

func TestDecodeTrustedPhonesKnownContainersOnly(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		count      int
	}{
		{"captured class boot", smsOptionsFixture, 1},
		{"JSON state", `{"trustedPhoneNumbers":[{"id":"3","lastTwoDigits":"ab"},{"id":3},{"id":4,"pushMode":"voice"}]}`, 1},
		{"reference app config", `<script id="app_config" type="application/json">{"bootData":{"twoSV":{"trustedDeviceVerification":{"phoneNumberVerification":{"trustedPhoneNumbers":[{"id":7}]}}}}}</script>`, 1},
		{"unknown root", `{"unrelated":{"trustedPhoneNumbers":[{"id":7}]}}`, 0},
		{"translation decoy", `{"i18n":{"trustedPhoneNumbers":[{"id":7}]}}`, 0},
		{"hidden resend retains phone for verification", `{"phoneNumberVerification":{"hideSendSMSCodeOption":true,"trustedPhoneNumbers":[{"id":7}]}}`, 1},
		{"bad ids", `{"trustedPhoneNumbers":[{"id":-1},{"id":"bad"},{"id":1.5},{"id":9007199254740992},{"id":null}]}`, 0},
		{"malformed", `<script class="boot_args" type="application/json">broken</script>`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeTrustedPhones([]byte(tc.body))
			if len(got) != tc.count {
				t.Fatalf("phone count=%d want=%d", len(got), tc.count)
			}
		})
	}
}

func TestWebSMSDeliveryFailuresAreNotReplayedOrHidden(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		transport bool
	}{
		{"throttle", 429, `{}`, false}, {"outage", 503, `{}`, false}, {"business failure", 200, `{"serviceErrors":[{"code":"123"}]}`, false}, {"invalid body", 200, `broken`, false}, {"negative success", 200, `{"success":false}`, false}, {"disconnect", 0, ``, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c := accountClient(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.transport {
					return nil, errors.New("synthetic disconnect")
				}
				res := accountResp(tc.status, tc.body)
				res.Header.Set("Retry-After", "120")
				return res, nil
			})
			original := Session{Region: RegionGlobal, WebVerification: &WebVerification{Phones: []TrustedPhone{{ID: 7}}}}
			updated, err := c.RequestWebVerification(context.Background(), original, "sms", 7)
			if err == nil || calls != 1 || updated.WebVerification.Sent || !original.WebVerification.RequestAfter.IsZero() {
				t.Fatal("failure swallowed, replayed, or input session mutated")
			}
			until := updated.WebVerification.RequestAfter
			if !tc.transport && until.Before(time.Now().Add(119*time.Second)) {
				t.Fatal("Retry-After shortened")
			}
			_, again := c.RequestWebVerification(context.Background(), updated, "sms", 7)
			if !IsRateLimited(again) || calls != 1 {
				t.Fatal("delivery failure resend was not delayed")
			}
		})
	}
}

func TestWebSMSMultipleNumbersRequireSelectionAndRejectUnknownID(t *testing.T) {
	fixture := newAuthFixture(t, http.StatusConflict)
	sends := 0
	c, _ := NewClient(Config{Random: deterministicRandom(), Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/appleauth/auth" {
			return testResponse(r, 200, `{"trustedPhoneNumbers":[{"id":2},{"id":7}]}`, nil), nil
		}
		if r.URL.Path == "/appleauth/auth/verify/phone" {
			sends++
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), `"id":7`) {
				t.Fatal("wrong selected phone")
			}
			return testResponse(r, 200, `{}`, nil), nil
		}
		return fixture.RoundTrip(r)
	})})
	s, pending, err := c.SignIn(context.Background(), fixture.appleID, fixture.password, RegionGlobal, nil)
	if err != nil || !pending || s.WebVerification.Method != "" || sends != 0 {
		t.Fatal("sent without a choice")
	}
	_, err = c.RequestWebVerification(context.Background(), s, "sms", 99)
	if !errors.Is(err, ErrInvalidConfig) || sends != 0 {
		t.Fatal("unadvertised phone reached network")
	}
	s, err = c.RequestWebVerification(context.Background(), s, "sms", 7)
	if err != nil || sends != 1 || !s.WebVerification.Sent || s.WebVerification.PhoneID != 7 {
		t.Fatalf("selected send err=%v", err)
	}
}

func TestWebDeviceDeliveryFailurePreservesPendingChallengeWithWarning(t *testing.T) {
	fixture := newAuthFixture(t, http.StatusConflict)
	c, _ := NewClient(Config{Random: deterministicRandom(), Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/appleauth/auth" {
			return testResponse(r, 200, `{}`, nil), nil
		}
		if r.URL.Path == "/appleauth/auth/verify/trusteddevice/securitycode" {
			return testResponse(r, 503, `{}`, nil), nil
		}
		return fixture.RoundTrip(r)
	})})
	session, pending, err := c.SignIn(context.Background(), fixture.appleID, fixture.password, RegionGlobal, nil)
	if err != nil || !pending || session.WebVerification == nil || !session.WebVerification.DeliveryFailed || session.WebVerification.Sent || session.WebVerification.DeliveryStatus != 503 {
		t.Fatal("failed device delivery must preserve pending challenge with explicit failure metadata")
	}
}

func TestWebVerificationScansAllBootScriptsAndRetainsHiddenSelection(t *testing.T) {
	body := `<script id="app_config">{"i18n":{"unrelated":true}}</script><script class="boot_args" type="application/json">{"direct":{"twoSV":{"phoneNumberVerification":{"hideSendSMSCodeOption":true,"trustedPhoneNumber":{"id":7,"pushMode":"sms"},"success":1}}}}</script>`
	v := decodeWebVerification([]byte(body))
	if len(v.Phones) != 1 || !v.SMSHidden || !v.SelectedPhonePresent || v.PhoneID != 7 || !v.AutomaticSMS {
		t.Fatalf("options=%+v", v)
	}
}

func TestWebVerifyAlreadyReceivedSMSDoesNotRequireConfirmedSend(t *testing.T) {
	fixture := newAuthFixture(t, http.StatusConflict)
	posts := 0
	c, _ := NewClient(Config{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/appleauth/auth/verify/phone/securitycode" {
			posts++
			return testResponse(r, 200, `{}`, http.Header{"X-Apple-Session-Token": []string{"session-token"}}), nil
		}
		if r.URL.Path == "/appleauth/auth/verify/phone" || r.URL.Path == "/appleauth/auth/verify/trusteddevice/securitycode" {
			t.Fatal("verification resent a code")
		}
		return fixture.RoundTrip(r)
	})})
	session := Session{Region: RegionGlobal, WebVerification: &WebVerification{Method: "sms", PhoneID: 7, Phones: []TrustedPhone{{ID: 7}}, Sent: false, SMSHidden: true}}
	_, err := c.VerifyCode(context.Background(), session, "123456")
	if err != nil || posts != 1 {
		t.Fatalf("SMS verify err=%v posts=%d", err, posts)
	}
}

func TestWebDevice412PhoneStageRetainsLoginWithoutRetrying(t *testing.T) {
	fixture := newAuthFixture(t, http.StatusConflict)
	sends := 0
	c, _ := NewClient(Config{Random: deterministicRandom(), Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/appleauth/auth" {
			return testResponse(r, 200, `{}`, nil), nil
		}
		if r.URL.Path == "/appleauth/auth/verify/trusteddevice/securitycode" {
			sends++
			return testResponse(r, 412, `{"phoneNumberVerification":{"trustedPhoneNumber":{"id":7,"pushMode":"sms"},"hideSendSMSCodeOption":true}}`, nil), nil
		}
		return fixture.RoundTrip(r)
	})})
	session, pending, err := c.SignIn(context.Background(), fixture.appleID, fixture.password, RegionGlobal, nil)
	v := session.WebVerification
	if err != nil || !pending || v == nil || v.Method != "sms" || v.PhoneID != 7 || !v.DeliveryFailed || v.DeliveryStatus != 412 || sends != 1 {
		t.Fatalf("pending=%v state=%+v err=%v", pending, v, err)
	}
}
