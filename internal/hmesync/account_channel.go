package hmesync

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

const (
	CodeAccountLoginRequired  = "APPLE_ACCOUNT_LOGIN_REQUIRED"
	CodeAccountSessionExpired = "APPLE_ACCOUNT_SESSION_EXPIRED"
)

type accountClient interface {
	SignInAccount(context.Context, string, string, apple.Region, *apple.AccountSession) (apple.AccountSession, bool, error)
	VerifyAccountCode(context.Context, apple.AccountSession, string) (apple.AccountSession, error)
	CreateAccountAlias(context.Context, apple.AccountSession, string, string) (apple.Alias, apple.AccountSession, error)
}

type accountAuthChallenge struct {
	id       string
	owner    int64
	identity accountIdentity
	webDSID  string
	session  apple.AccountSession
	expires  time.Time
	attempts int
}

func newChallengeID() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func accountSessionInfo(session *apple.AccountSession) SessionInfo {
	if session == nil || session.APIKey == "" || session.AuthenticatedAt.IsZero() {
		return SessionInfo{Status: StatusLoginRequired}
	}
	return SessionInfo{Status: StatusAuthenticated, AppleID: session.AppleID, Region: publicRegion(string(session.Region)), AuthenticatedAt: &session.AuthenticatedAt, ExpiresAt: &session.ExpiresAt}
}

func (s *Service) GetAccountSession(ctx context.Context, id int64) (SessionInfo, error) {
	_, session, err := s.loadSession(ctx, id)
	if errors.Is(err, ErrLoginRequired) {
		return SessionInfo{Status: StatusLoginRequired}, nil
	}
	if err != nil {
		return SessionInfo{}, err
	}
	return accountSessionInfo(session.Account), nil
}

// Refresh under the same account lock as creation, so waiting batches retain
// management cookies without overwriting concurrent Web-session changes.
func (s *Service) KeepAliveAccountSession(ctx context.Context, id int64) error {
	client, ok := s.client.(interface {
		RefreshAccountSession(context.Context, apple.AccountSession) (apple.AccountSession, error)
	})
	if !ok {
		return nil
	}
	release, err := s.acquireOperation(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil || !account.Enabled {
		return err
	}
	if domain.IsIMAPAuthenticationFailure(account.LastSyncError) {
		return domain.ErrIMAPAuthenticationPaused
	}
	_, web, err := s.loadSession(ctx, id)
	if errors.Is(err, ErrLoginRequired) {
		return nil
	}
	if err != nil {
		return err
	}
	if accountSessionInfo(web.Account).Status != StatusAuthenticated {
		return nil
	}
	managed, err := client.RefreshAccountSession(ctx, *web.Account)
	if err != nil {
		return err
	}
	if !sameEmail(managed.AppleID, web.AppleID) {
		return wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
	}
	web.Account = &managed
	_, err = s.persistSession(ctx, id, identityOf(account), web)
	return err
}

func (s *Service) StartAccountAuth(ctx context.Context, owner, id int64, appleID, password string, region apple.Region) (AuthResult, error) {
	client, ok := s.client.(accountClient)
	if !ok {
		return AuthResult{}, wrapError(CodeUpstreamError, ErrUpstream, nil)
	}
	release, err := s.acquireOperation(ctx, id)
	if err != nil {
		return AuthResult{}, err
	}
	defer release()
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return AuthResult{}, err
	}
	if !account.Enabled {
		return AuthResult{}, wrapError(CodeAccountDisabled, ErrAccountDisabled, nil)
	}
	_, web, err := s.loadSession(ctx, id)
	if err != nil {
		return AuthResult{}, err
	}
	if owner < 1 || !sameEmail(appleID, web.AppleID) {
		return AuthResult{}, wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
	}
	region, err = normalizeRegion(region)
	if err != nil {
		return AuthResult{}, err
	}
	managed, verify, err := client.SignInAccount(ctx, appleID, password, region, web.Account)
	password = ""
	if err != nil {
		return AuthResult{}, mapAppleError(err, false)
	}
	if !sameEmail(managed.AppleID, web.AppleID) {
		return AuthResult{}, wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
	}
	s.challengeMu.Lock()
	if s.accountAuthChallenges == nil {
		s.accountAuthChallenges = make(map[int64]accountAuthChallenge)
	}
	delete(s.accountAuthChallenges, id)
	s.challengeMu.Unlock()
	if verify {
		// Reuse the random challenge generator, but keep this flow separate so
		// a new management login never removes the working iCloud Web session.
		flowID, err := newChallengeID()
		if err != nil {
			return AuthResult{}, err
		}
		flow := accountAuthChallenge{id: flowID, owner: owner, identity: identityOf(account), webDSID: web.DSID, session: managed, expires: s.now().Add(s.challengeTTL)}
		s.challengeMu.Lock()
		s.accountAuthChallenges[id] = flow
		s.challengeMu.Unlock()
		return AuthResult{Status: StatusVerificationRequired, ChallengeID: flowID, Session: SessionInfo{Status: StatusVerificationRequired, AppleID: appleID, Region: string(region), ExpiresAt: &flow.expires}}, nil
	}
	if managed.APIKey == "" || managed.AuthenticatedAt.IsZero() {
		return AuthResult{}, wrapError(CodeAccountSessionExpired, ErrSessionExpired, nil)
	}
	web.Account = &managed
	if _, err := s.persistSession(ctx, id, identityOf(account), web); err != nil {
		return AuthResult{}, err
	}
	return AuthResult{Status: StatusAuthenticated, Session: accountSessionInfo(web.Account)}, nil
}

func (s *Service) VerifyAccountAuth(ctx context.Context, owner, id int64, challengeID, code string) (AuthResult, error) {
	client, ok := s.client.(accountClient)
	if !ok {
		return AuthResult{}, wrapError(CodeUpstreamError, ErrUpstream, nil)
	}
	release, err := s.acquireOperation(ctx, id)
	if err != nil {
		return AuthResult{}, err
	}
	defer release()
	s.challengeMu.Lock()
	flow, exists := s.accountAuthChallenges[id]
	if !exists || flow.owner != owner || flow.id != challengeID || !s.now().Before(flow.expires) || flow.attempts >= s.maxAttempts {
		s.challengeMu.Unlock()
		return AuthResult{}, wrapError(CodeFlowExpired, ErrFlowExpired, nil)
	}
	flow.attempts++
	s.accountAuthChallenges[id] = flow
	s.challengeMu.Unlock()
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return AuthResult{}, err
	}
	if !account.Enabled || !sameIdentity(flow.identity, identityOf(account)) {
		return AuthResult{}, wrapError(CodeAccountChanged, ErrAccountChanged, nil)
	}
	_, web, err := s.loadSession(ctx, id)
	if err != nil {
		return AuthResult{}, err
	}
	if web.DSID != flow.webDSID || !sameEmail(web.AppleID, flow.session.AppleID) {
		return AuthResult{}, wrapError(CodeAccountChanged, ErrAccountChanged, nil)
	}
	managed, err := client.VerifyAccountCode(ctx, flow.session, code)
	if err != nil {
		flow.session = managed
		s.challengeMu.Lock()
		s.accountAuthChallenges[id] = flow
		s.challengeMu.Unlock()
		return AuthResult{}, mapAppleError(err, true)
	}
	if !sameEmail(managed.AppleID, web.AppleID) || managed.APIKey == "" || managed.AuthenticatedAt.IsZero() {
		return AuthResult{}, wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
	}
	web.Account = &managed
	if _, err := s.persistSession(ctx, id, identityOf(account), web); err != nil {
		return AuthResult{}, err
	}
	s.challengeMu.Lock()
	delete(s.accountAuthChallenges, id)
	s.challengeMu.Unlock()
	return AuthResult{Status: StatusAuthenticated, Session: accountSessionInfo(web.Account)}, nil
}

func (s *Service) ClearAccountAuth(ctx context.Context, id int64) error {
	release, err := s.acquireOperation(ctx, id)
	if err != nil {
		return err
	}
	defer release()
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	s.challengeMu.Lock()
	delete(s.accountAuthChallenges, id)
	s.challengeMu.Unlock()
	_, web, err := s.loadSession(ctx, id)
	if errors.Is(err, ErrLoginRequired) || errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	web.Account = nil
	_, err = s.persistSession(ctx, id, identityOf(account), web)
	return err
}

// Called under the account operation lock. Both APIs share the cooldown;
// a throttled or uncertain completion never changes channels.
func (s *Service) createRemoteAliasWithChannel(ctx context.Context, id int64, web apple.Session, legacy AutoAliasClient, channel string) (apple.Alias, apple.Session, error) {
	return s.createRemoteAliasWithMode(ctx, id, web, legacy, channel, false)
}

func (s *Service) createRemoteAliasWithMode(ctx context.Context, id int64, web apple.Session, legacy AutoAliasClient, channel string, probe bool) (apple.Alias, apple.Session, error) {
	s.operationMu.Lock()
	if s.creationCooldowns == nil {
		s.creationCooldowns = make(map[int64]map[string]time.Time)
	}
	if s.creationCooldowns[id] == nil {
		s.creationCooldowns[id] = make(map[string]time.Time)
	}
	cooldowns := s.creationCooldowns[id]
	s.operationMu.Unlock()
	channels := []string{channel}
	if channel == "auto" {
		channels = []string{"icloud_web"}
		if web.Account != nil && web.Account.APIKey != "" {
			channels = []string{"apple_account"}
		}
	}
	var earliest time.Time
	for _, selected := range channels {
		until := cooldowns["apple_account"]
		if webUntil := cooldowns["icloud_web"]; webUntil.After(until) {
			until = webUntil
		}
		if !probe && s.now().Before(until) {
			if earliest.IsZero() || until.Before(earliest) {
				earliest = until
			}
			continue
		}
		var alias apple.Alias
		var err error
		if selected == "apple_account" {
			client, ok := s.client.(accountClient)
			if !ok || web.Account == nil || web.Account.APIKey == "" {
				return alias, web, wrapError(CodeAccountLoginRequired, ErrLoginRequired, nil)
			}
			if !sameEmail(web.Account.AppleID, web.AppleID) {
				return alias, web, wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
			}
			var managed apple.AccountSession
			alias, managed, err = client.CreateAccountAlias(ctx, *web.Account, autoCreateLabel, autoCreateNote)
			web.Account = &managed
			if errors.Is(err, apple.ErrInvalidSession) {
				// Keep Web authentication available for synchronization and OTP.
				err = wrapError(CodeAccountSessionExpired, ErrUpstream, err)
			}
		} else {
			alias, web, err = legacy.CreateAlias(ctx, web, autoCreateLabel, autoCreateNote)
		}
		if probe || err == nil || strings.TrimSpace(alias.HME) != "" || !apple.IsRateLimited(err) {
			return alias, web, err
		}
		until = s.now().Add(max(24*time.Hour, apple.RetryDelay(err)))
		cooldowns["apple_account"] = until
		cooldowns["icloud_web"] = until
		if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	return apple.Alias{}, web, &apple.Error{Op: "creation channels cooling down", Kind: apple.ErrService, StatusCode: http.StatusTooManyRequests, RetryAfter: max(time.Second, earliest.Sub(s.now()))}
}
