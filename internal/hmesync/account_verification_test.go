package hmesync

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"icloud-api/internal/apple"
)

type accountSMSClient struct {
	accountTestClient
	send   func(context.Context, apple.AccountSession, string, int64) (apple.AccountSession, error)
	verify func(context.Context, apple.AccountSession, string) (apple.AccountSession, error)
}

func (c *accountSMSClient) RequestAccountVerification(ctx context.Context, s apple.AccountSession, method string, id int64) (apple.AccountSession, error) {
	return c.send(ctx, s, method, id)
}
func (c *accountSMSClient) VerifyAccountCode(ctx context.Context, s apple.AccountSession, code string) (apple.AccountSession, error) {
	return c.verify(ctx, s, code)
}

func TestAccountSMSChallengeSelectionOwnerExpiryAndFailedSend(t *testing.T) {
	s, _, account := independentAccountFixture(t, true)
	ctx := context.Background()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	sends, verifies := 0, 0
	c := &accountSMSClient{}
	c.accountSignIn = func(managed apple.AccountSession) (apple.AccountSession, bool, error) {
		managed.SCNT = "initial"
		managed.Verification = &apple.WebVerification{Method: "device", Phones: []apple.TrustedPhone{{ID: 7}, {ID: 9}}}
		return managed, true, nil
	}
	c.send = func(_ context.Context, managed apple.AccountSession, method string, id int64) (apple.AccountSession, error) {
		sends++
		if method != "sms" || id != 7 {
			t.Fatal("wrong selection")
		}
		v := *managed.Verification
		v.RequestAfter = now.Add(2 * time.Minute)
		v.DeliveryFailed = true
		managed.Verification = &v
		managed.SCNT = "after-error"
		return managed, &apple.Error{Kind: apple.ErrService, StatusCode: http.StatusTooManyRequests, RetryAfter: 2 * time.Minute}
	}
	c.verify = func(_ context.Context, managed apple.AccountSession, code string) (apple.AccountSession, error) {
		verifies++
		if code != "123456" || managed.Verification.Method != "sms" || managed.Verification.PhoneID != 7 || managed.SCNT != "after-error" {
			t.Fatal("wrong verification flow")
		}
		managed.APIKey = "completed-key"
		managed.AuthenticatedAt = now
		managed.Verification = nil
		return managed, nil
	}
	s.client = c
	start, err := s.StartAccountAuth(ctx, 5, account.ID, account.Email, "fixture", apple.RegionGlobal)
	if err != nil || start.Verification == nil || len(start.Verification.Phones) != 2 {
		t.Fatalf("start err=%v", err)
	}
	if _, err = s.RequestAccountAuthCode(ctx, 6, account.ID, start.ChallengeID, "sms", 7); !errors.Is(err, ErrFlowExpired) || sends != 0 {
		t.Fatal("other owner reached sender")
	}
	if _, err = s.RequestAccountAuthCode(ctx, 5, account.ID, start.ChallengeID, "sms", 99); Code(err) != CodeVerificationMethodUnavailable || sends != 0 {
		t.Fatal("unknown phone reached sender")
	}
	_, err = s.RequestAccountAuthCode(ctx, 5, account.ID, start.ChallengeID, "sms", 7)
	if !errors.Is(err, ErrRateLimited) || sends != 1 {
		t.Fatalf("send err=%v", err)
	}
	if _, err = s.VerifyAccountAuthWithMethod(ctx, 5, account.ID, start.ChallengeID, "123456", "sms", 99); Code(err) != CodeVerificationMethodUnavailable || verifies != 0 {
		t.Fatal("unknown phone reached verification")
	}
	result, err := s.VerifyAccountAuthWithMethod(ctx, 5, account.ID, start.ChallengeID, "123456", "sms", 7)
	if err != nil || result.Status != StatusAuthenticated || verifies != 1 || sends != 1 {
		t.Fatalf("verify err=%v", err)
	}
	if _, err = s.RequestAccountAuthCode(ctx, 5, account.ID, start.ChallengeID, "sms", 7); !errors.Is(err, ErrFlowExpired) {
		t.Fatal("completed challenge replayed")
	}
	start, err = s.StartAccountAuth(ctx, 5, account.ID, account.Email, "fixture", apple.RegionGlobal)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Minute)
	if _, err = s.RequestAccountAuthCode(ctx, 5, account.ID, start.ChallengeID, "sms", 7); !errors.Is(err, ErrFlowExpired) || sends != 1 {
		t.Fatal("expired challenge reached sender")
	}
}
