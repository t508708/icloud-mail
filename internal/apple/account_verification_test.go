package apple

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestAccountSMSSendVerifyTrustAndRefreshUseAccountWidget(t *testing.T) {
	sends, verifies, trusts := 0, 0, 0
	c := accountClient(t, func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/appleauth/auth/verify/phone":
			sends++
			var body struct {
				Phone map[string]any `json:"phoneNumber"`
				Mode  string         `json:"mode"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || r.Method != "PUT" || body.Mode != "sms" || body.Phone["id"] != float64(7) || len(body.Phone) != 1 || r.Header.Get("X-Apple-Widget-Key") != accountWidget {
				t.Fatal("wrong Account SMS request")
			}
			res := accountResp(200, `{}`)
			res.Header.Set("scnt", "sent-scnt")
			return res, nil
		case "/appleauth/auth/verify/phone/securitycode":
			verifies++
			var body struct {
				Phone map[string]any `json:"phoneNumber"`
				Mode  string         `json:"mode"`
				Code  struct {
					Code string `json:"code"`
				} `json:"securityCode"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || r.Method != "POST" || body.Phone["id"] != float64(7) || body.Phone["nonFTEU"] != true || body.Mode != "sms" || body.Code.Code != "123456" || r.Header.Get("scnt") != "sent-scnt" || r.Header.Get("X-Apple-Widget-Key") != accountWidget {
				t.Fatal("wrong Account SMS verification")
			}
			return accountResp(204, ""), nil
		case "/appleauth/auth/2sv/trust":
			trusts++
			return accountResp(204, ""), nil
		case "/account/manage/gs/ws/token":
			return accountResp(200, `{"timeOutInterval":30}`), nil
		case "/account/manage":
			return accountResp(200, `{"apiKey":"fixture-key"}`), nil
		case "/account/manage/forwardemail":
			return accountResp(200, `{}`), nil
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
			return nil, nil
		}
	})
	flag := true
	original := AccountSession{AppleID: "fixture@example.invalid", SCNT: "initial", FrameID: "frame", Verification: &WebVerification{Method: "sms", PhoneID: 7, Phones: []TrustedPhone{{ID: 7, NonFTEU: &flag}}}}
	s, err := c.RequestAccountVerification(context.Background(), original, "sms", 7)
	if err != nil || sends != 1 || !s.Verification.Sent || original.Verification.Sent || s.SCNT != "sent-scnt" {
		t.Fatalf("send err=%v", err)
	}
	_, err = c.RequestAccountVerification(context.Background(), s, "sms", 7)
	if !IsRateLimited(err) || sends != 1 {
		t.Fatal("Account SMS resend bypassed cooldown")
	}
	s, err = c.VerifyAccountCode(context.Background(), s, "123456")
	if err != nil || verifies != 1 || trusts != 1 || s.APIKey != "fixture-key" || s.Verification != nil {
		t.Fatalf("verify err=%v", err)
	}
}

func TestAccountSMSFailedCodeKeepsRotatedPendingState(t *testing.T) {
	c := accountClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/appleauth/auth/verify/phone/securitycode" {
			t.Fatal("unexpected request")
		}
		res := accountResp(400, `{"errorCode":-21669}`)
		res.Header.Set("scnt", "rotated-after-error")
		res.Header.Set("Set-Cookie", "pending=rotated; Path=/; Secure")
		return res, nil
	})
	s, err := c.VerifyAccountCode(context.Background(), AccountSession{SCNT: "initial", FrameID: "frame", Verification: &WebVerification{Method: "sms", PhoneID: 7, Phones: []TrustedPhone{{ID: 7}}}}, "123456")
	if !errors.Is(err, ErrTwoFactorCode) || s.SCNT != "rotated-after-error" || len(s.Cookies) == 0 || s.Verification == nil {
		t.Fatal("failed code lost pending state")
	}
}

func TestAccountSMSFailureRetainsDelayWithoutChangingAuthenticatedSession(t *testing.T) {
	calls := 0
	c := accountClient(t, func(*http.Request) (*http.Response, error) {
		calls++
		res := accountResp(429, `{}`)
		res.Header.Set("Retry-After", "120")
		return res, nil
	})
	original := AccountSession{SCNT: "initial", FrameID: "frame", Verification: &WebVerification{Phones: []TrustedPhone{{ID: 7}}}}
	s, err := c.RequestAccountVerification(context.Background(), original, "sms", 7)
	if err == nil || !IsRateLimited(err) || calls != 1 || !s.Verification.DeliveryFailed || s.Verification.RequestAfter.Before(time.Now().Add(119*time.Second)) {
		t.Fatal("Account failed send lost delay")
	}
	_, err = c.RequestAccountVerification(context.Background(), s, "sms", 7)
	if !IsRateLimited(err) || calls != 1 {
		t.Fatal("failed send was replayed")
	}
	_, err = c.RequestAccountVerification(context.Background(), original, "sms", 99)
	if !errors.Is(err, ErrInvalidConfig) || calls != 1 {
		t.Fatal("unknown phone reached network")
	}
}
