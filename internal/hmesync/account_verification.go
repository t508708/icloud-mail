package hmesync

import (
	"context"
	"errors"
	"icloud-api/internal/apple"
	"icloud-api/internal/store"
)

func selectVerification(v *apple.WebVerification, method string, phoneID int64) (*apple.WebVerification, error) {
	if v == nil || method != "sms" && method != "device" {
		return nil, wrapError(CodeVerificationMethodUnavailable, ErrUpstream, nil)
	}
	if method == "sms" {
		found := false
		for _, p := range v.Phones {
			found = found || p.ID == phoneID
		}
		if !found {
			return nil, wrapError(CodeVerificationMethodUnavailable, ErrUpstream, nil)
		}
	}
	copy := *v
	copy.Method, copy.PhoneID = method, phoneID
	return &copy, nil
}

func (s *Service) RequestAccountAuthCode(ctx context.Context, owner, id int64, challengeID, method string, phoneID int64) (AuthResult, error) {
	release, err := s.acquireOperation(ctx, id)
	if err != nil {
		return AuthResult{}, err
	}
	defer release()
	s.challengeMu.Lock()
	flow, exists := s.accountAuthChallenges[id]
	s.challengeMu.Unlock()
	if !exists || flow.owner != owner || flow.id != challengeID || !s.now().Before(flow.expires) || flow.attempts >= s.maxAttempts {
		return AuthResult{}, wrapError(CodeFlowExpired, ErrFlowExpired, nil)
	}
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return AuthResult{}, err
	}
	if !sameIdentity(flow.identity, identityOf(account)) {
		return AuthResult{}, wrapError(CodeAccountChanged, ErrAccountChanged, nil)
	}
	if !account.Enabled {
		return AuthResult{}, wrapError(CodeAccountDisabled, ErrAccountDisabled, nil)
	}
	web, webErr := s.repo.GetAppleWebSession(ctx, id)
	if webErr != nil && !errors.Is(webErr, store.ErrNotFound) {
		return AuthResult{}, webErr
	}
	if webErr == nil && !sameEmail(web.AppleID, flow.session.AppleID) {
		return AuthResult{}, wrapError(CodeAccountChanged, ErrAccountChanged, nil)
	}
	if _, err := selectVerification(flow.session.Verification, method, phoneID); err != nil {
		return AuthResult{}, err
	}
	client, ok := s.client.(interface {
		RequestAccountVerification(context.Context, apple.AccountSession, string, int64) (apple.AccountSession, error)
	})
	if !ok {
		return AuthResult{}, wrapError(CodeUpstreamError, ErrUpstream, nil)
	}
	updated, sendErr := client.RequestAccountVerification(ctx, flow.session, method, phoneID)
	flow.session = updated
	s.challengeMu.Lock()
	s.accountAuthChallenges[id] = flow
	s.challengeMu.Unlock()
	result := AuthResult{Status: StatusVerificationRequired, ChallengeID: flow.id, Verification: verificationInfo(updated.Verification), Session: SessionInfo{Status: StatusVerificationRequired, AppleID: updated.AppleID, Region: string(updated.Region), ExpiresAt: &flow.expires}}
	if sendErr != nil {
		return result, mapAppleError(sendErr, false)
	}
	return result, nil
}
