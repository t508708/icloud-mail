package hmesync

import (
	"context"
	"fmt"

	"icloud-api/internal/apple"
)

func webVerificationInfo(session apple.Session) *WebVerificationInfo {
	return verificationInfo(session.WebVerification)
}

func verificationInfo(v *apple.WebVerification) *WebVerificationInfo {
	if v == nil {
		return nil
	}
	info := &WebVerificationInfo{Method: v.Method, Sent: v.Sent, PhoneID: v.PhoneID,
		DiscoveryStatus: v.DiscoveryStatus, DeliveryStatus: v.DeliveryStatus,
		OptionsSource: v.OptionsSource, PhoneListPresent: v.PhoneListPresent, RawPhoneCount: v.RawPhoneCount,
		InvalidPhoneCount: v.InvalidPhoneCount, SMSHidden: v.SMSHidden, AutomaticSMS: v.AutomaticSMS,
		Phones: make([]WebVerificationPhone, 0, len(v.Phones))}
	if v.DeliveryFailed {
		info.DeliveryWarning = "Apple 未确认本次验证码发送，登录流程已保留。如果已经收到验证码，可继续填写；请勿连续重新登录或反复发送。"
	}
	if !v.RequestAfter.IsZero() {
		after := v.RequestAfter
		info.RequestAfter = &after
	}
	for i, p := range v.Phones {
		label := fmt.Sprintf("受信任手机号 %d", i+1)
		if len(p.LastTwoDigits) == 2 && p.LastTwoDigits[0] >= '0' && p.LastTwoDigits[0] <= '9' && p.LastTwoDigits[1] >= '0' && p.LastTwoDigits[1] <= '9' {
			label += "（尾号 " + p.LastTwoDigits + "）"
		}
		info.Phones = append(info.Phones, WebVerificationPhone{ID: p.ID, Label: label})
	}
	return info
}

// RequestAuthCode uses the same owner and account identity checks as VerifyAuth.
// It updates the existing challenge even on delivery failure to preserve cooldown.
func (s *Service) RequestAuthCode(ctx context.Context, owner, accountID int64, challengeID, method string, phoneID int64) (AuthResult, error) {
	release, err := s.acquireOperation(ctx, accountID)
	if err != nil {
		return AuthResult{}, err
	}
	defer release()
	flow, ok := s.readChallenge(challengeID, owner, accountID)
	if !ok {
		return AuthResult{}, wrapError(CodeFlowExpired, ErrFlowExpired, nil)
	}
	account, err := s.repo.GetAccount(ctx, accountID)
	if err != nil {
		return AuthResult{}, err
	}
	appleCtx, err := s.accountProxyForAccount(ctx, account)
	if err != nil {
		return AuthResult{}, err
	}
	if !sameIdentity(identityOf(account), flow.identity) || !sameAccountProxy(account.Proxy, flow.proxy) {
		s.deleteChallenge(challengeID)
		return AuthResult{}, wrapError(CodeAccountChanged, ErrAccountChanged, nil)
	}
	if !account.Enabled {
		return AuthResult{}, wrapError(CodeAccountDisabled, ErrAccountDisabled, nil)
	}
	if method != "device" && method != "sms" {
		return AuthResult{}, wrapError(CodeVerificationMethodUnavailable, ErrUpstream, nil)
	}
	if method == "sms" {
		found := false
		if v := flow.session.WebVerification; v != nil {
			for _, p := range v.Phones {
				found = found || p.ID == phoneID
			}
		}
		if !found {
			return AuthResult{}, wrapError(CodeVerificationMethodUnavailable, ErrUpstream, nil)
		}
	}
	client, ok := s.client.(interface {
		RequestWebVerification(context.Context, apple.Session, string, int64) (apple.Session, error)
	})
	if !ok {
		return AuthResult{}, wrapError(CodeUpstreamError, ErrUpstream, nil)
	}
	updated, sendErr := client.RequestWebVerification(appleCtx, flow.session, method, phoneID)
	flow.session = updated
	s.challengeMu.Lock()
	if current, exists := s.challenges[challengeID]; exists && current.ownerAdminID == owner && current.accountID == accountID {
		s.challenges[challengeID] = flow
	}
	s.challengeMu.Unlock()
	info := sessionInfoFromSession(updated, StatusVerificationRequired, nil, &flow.expiresAt)
	result := AuthResult{Status: StatusVerificationRequired, ChallengeID: flow.id, Session: info, Verification: webVerificationInfo(updated)}
	if sendErr != nil {
		return result, mapAppleError(sendErr, false)
	}
	return result, nil
}
