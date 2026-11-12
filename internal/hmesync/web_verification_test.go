package hmesync

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
)

type webCodeTestClient struct {
	fakeAppleClient
	send func(context.Context, apple.Session, string, int64) (apple.Session, error)
}

func (c *webCodeTestClient) RequestWebVerification(ctx context.Context, s apple.Session, method string, id int64) (apple.Session, error) {
	return c.send(ctx, s, method, id)
}

func TestWebCodeChallengeOwnershipExpiryAndSendFailureState(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	repo := newFakeRepository(domain.Account{ID: 12, Email: "owner@icloud.com", Enabled: true}, now)
	calls := 0
	client := &webCodeTestClient{}
	client.signIn = func(context.Context, string, string, apple.Region, *apple.Session) (apple.Session, bool, error) {
		return apple.Session{AppleID: "owner@icloud.com", Region: apple.RegionGlobal, WebVerification: &apple.WebVerification{Phones: []apple.TrustedPhone{{ID: 7, LastTwoDigits: "42"}, {ID: 9}}, Method: ""}}, true, nil
	}
	client.send = func(_ context.Context, s apple.Session, method string, id int64) (apple.Session, error) {
		calls++
		if method != "sms" || id != 7 {
			t.Fatal("invalid selection reached sender")
		}
		v := *s.WebVerification
		v.Method = "sms"
		v.PhoneID = 7
		v.Sent = false
		v.RequestAfter = now.Add(2 * time.Minute)
		s.WebVerification = &v
		return s, &apple.Error{Op: "request sms two-factor code", Kind: apple.ErrService, StatusCode: 429, RetryAfter: 2 * time.Minute}
	}
	s := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
	started, err := s.StartAuth(ctx, 5, 12, "owner@icloud.com", "fixture", apple.RegionGlobal)
	if err != nil || started.Verification == nil || !strings.Contains(started.Verification.Phones[0].Label, "42") {
		t.Fatalf("start err=%v", err)
	}
	if _, err = s.RequestAuthCode(ctx, 6, 12, started.ChallengeID, "sms", 7); !errors.Is(err, ErrFlowExpired) || calls != 0 {
		t.Fatal("other administrator reached sender")
	}
	if _, err = s.RequestAuthCode(ctx, 5, 13, started.ChallengeID, "sms", 7); !errors.Is(err, ErrFlowExpired) || calls != 0 {
		t.Fatal("other account reached sender")
	}
	if _, err = s.RequestAuthCode(ctx, 5, 12, started.ChallengeID, "sms", 99); Code(err) != CodeVerificationMethodUnavailable || calls != 0 {
		t.Fatal("unknown number reached sender")
	}
	_, err = s.RequestAuthCode(ctx, 5, 12, started.ChallengeID, "sms", 7)
	if !errors.Is(err, ErrRateLimited) || calls != 1 {
		t.Fatalf("send err=%v", err)
	}
	flow, ok := s.readChallenge(started.ChallengeID, 5, 12)
	if !ok || !flow.session.WebVerification.RequestAfter.Equal(now.Add(2*time.Minute)) || flow.session.WebVerification.Sent {
		t.Fatal("lost failed send cooldown")
	}
	encoded, _ := json.Marshal(started.Verification)
	if strings.Contains(string(encoded), "fixture") || strings.Contains(string(encoded), "owner@icloud.com") {
		t.Fatal("verification info leaked private data")
	}
	now = now.Add(11 * time.Minute)
	if _, err = s.RequestAuthCode(ctx, 5, 12, started.ChallengeID, "sms", 7); !errors.Is(err, ErrFlowExpired) || calls != 1 {
		t.Fatal("expired challenge reached sender")
	}
}

func TestAutomaticLoginDoesNotPickAmongMultiplePhones(t *testing.T) {
	s, _, account, _, client := automaticLoginFixture(t)
	client.signIn = func(context.Context, string, string, apple.Region, *apple.Session) (apple.Session, bool, error) {
		return apple.Session{AppleID: account.Email, WebVerification: &apple.WebVerification{Phones: []apple.TrustedPhone{{ID: 1}, {ID: 2}}}}, true, nil
	}
	err := s.startAutomaticLogin(context.Background(), &automaticLoginAttempt{account: account, appleID: account.Email, channel: "icloud_web"})
	if Code(err) != CodePhoneSelectionRequired || !errors.Is(err, ErrAccountActionRequired) {
		t.Fatalf("selection error=%v", err)
	}
}

func TestWebAlreadyReceivedSMSCanBeSubmittedWithoutSendingAgain(t *testing.T) {
	now := time.Now().UTC()
	ctx := context.Background()
	repo := newFakeRepository(domain.Account{ID: 12, Email: "owner@icloud.com", Enabled: true}, now)
	client := &webCodeTestClient{}
	client.signIn = func(context.Context, string, string, apple.Region, *apple.Session) (apple.Session, bool, error) {
		return apple.Session{AppleID: "owner@icloud.com", Region: apple.RegionGlobal, DSID: "fixture-dsid", SessionToken: "fixture-token", WebVerification: &apple.WebVerification{Method: "device", DeliveryFailed: true, DeliveryStatus: 412, Phones: []apple.TrustedPhone{{ID: 7}}}}, true, nil
	}
	client.send = func(context.Context, apple.Session, string, int64) (apple.Session, error) {
		t.Fatal("verification sent another code")
		return apple.Session{}, nil
	}
	client.verify = func(_ context.Context, session apple.Session, code string) (apple.Session, error) {
		if code != "123456" || session.WebVerification.Method != "sms" || session.WebVerification.PhoneID != 7 {
			t.Fatal("wrong verification selection")
		}
		session.WebVerification = nil
		return session, nil
	}
	s := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
	start, err := s.StartAuth(ctx, 5, 12, "owner@icloud.com", "fixture", apple.RegionGlobal)
	if err != nil || start.Verification.DeliveryWarning == "" {
		t.Fatalf("pending warning missing err=%v", err)
	}
	_, err = s.VerifyAuthWithMethod(ctx, 5, 12, start.ChallengeID, "123456", "sms", 99)
	if Code(err) != CodeVerificationMethodUnavailable || client.verifyCalls.Load() != 0 {
		t.Fatal("unknown phone accepted")
	}
	result, err := s.VerifyAuthWithMethod(ctx, 5, 12, start.ChallengeID, "123456", "sms", 7)
	if err != nil || result.Status != StatusAuthenticated || client.verifyCalls.Load() != 1 {
		t.Fatalf("verify err=%v", err)
	}
}

func TestWebSMSSelectionSurvivesInvalidCode(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	repo := newFakeRepository(domain.Account{ID: 12, Email: "owner@icloud.com", Enabled: true}, now)
	client := &webCodeTestClient{}
	client.signIn = func(context.Context, string, string, apple.Region, *apple.Session) (apple.Session, bool, error) {
		return apple.Session{AppleID: "owner@icloud.com", Region: apple.RegionGlobal, WebVerification: &apple.WebVerification{Method: "device", Phones: []apple.TrustedPhone{{ID: 7}}}}, true, nil
	}
	verifyCalls := 0
	client.verify = func(_ context.Context, session apple.Session, code string) (apple.Session, error) {
		verifyCalls++
		if session.WebVerification.Method != "sms" || session.WebVerification.PhoneID != 7 {
			t.Fatalf("retry used wrong verification method: %#v", session.WebVerification)
		}
		if verifyCalls == 1 {
			return session, &apple.Error{Kind: apple.ErrTwoFactorCode, StatusCode: 400}
		}
		if code != "654321" {
			t.Fatalf("unexpected retry code: %s", code)
		}
		session.WebVerification = nil
		return session, nil
	}
	s := newTestService(t, repo, client, &fakeLocker{}, func() time.Time { return now })
	start, err := s.StartAuth(ctx, 5, 12, "owner@icloud.com", "fixture", apple.RegionGlobal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyAuthWithMethod(ctx, 5, 12, start.ChallengeID, "123456", "sms", 7); !errors.Is(err, ErrVerificationInvalid) {
		t.Fatalf("invalid SMS code err=%v", err)
	}
	result, err := s.VerifyAuth(ctx, 5, 12, start.ChallengeID, "654321")
	if err != nil || result.Status != StatusAuthenticated || verifyCalls != 2 {
		t.Fatalf("SMS retry status=%v calls=%d err=%v", result.Status, verifyCalls, err)
	}
}
