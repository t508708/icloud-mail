package hmesync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"icloud-api/internal/apple"
	"icloud-api/internal/autologincode"
	"icloud-api/internal/domain"
	"icloud-api/internal/secure"
	"icloud-api/internal/store"
)

const (
	codeAutoLoginChanged     = "AUTO_LOGIN_CONFIG_CHANGED"
	codeAutoLoginCredentials = "AUTO_LOGIN_CREDENTIALS_UNAVAILABLE"
	codeAutoLoginCodeService = "AUTO_LOGIN_CODE_SERVICE_UNAVAILABLE"
	codeAutoLoginCodeTimeout = "AUTO_LOGIN_NEW_CODE_TIMEOUT"
	codeAutoLoginSuperseded  = "AUTO_LOGIN_SUPERSEDED"
	codeAutoLoginCodeURL     = "AUTO_LOGIN_CODE_URL_INVALID"
)

type automaticLoginAttempt struct {
	config      domain.AccountAutoLoginConfig
	account     domain.Account
	channel     string
	appleID     string
	stamp       string
	started     time.Time
	credentials secure.AccountAutoLoginCredentials
	web         apple.Session
	managed     apple.AccountSession
	verify      bool
	state       domain.AccountAutoLoginState
	generation  uint64
	appleCtx    context.Context
}

func (s *Service) runAutomaticLoginChannel(ctx context.Context, config domain.AccountAutoLoginConfig, channel string, logger *slog.Logger) {
	if !s.automaticChannelDue(ctx, config, channel) {
		return
	}
	var attempt *automaticLoginAttempt
	err := s.WithAutoLoginSettingsLock(ctx, config.AccountID, func() error {
		var err error
		attempt, err = s.prepareAutomaticLogin(ctx, config, channel)
		return err
	})
	if err != nil || attempt == nil {
		return
	}
	defer func() { attempt.credentials = secure.AccountAutoLoginCredentials{} }()
	fetcher := s.autoLoginCodeFetcher
	if fetcher == nil {
		fetcher = autologincode.NewClient()
	}
	codeCtx := ctx
	if attempt.appleCtx != nil {
		if proxy, ok := apple.ProxyConfigFromContext(attempt.appleCtx); ok {
			codeCtx = autologincode.WithProxy(ctx, proxy)
		}
	}
	// Snapshot before triggering a challenge. Providers with no timestamp must
	// produce a different code; a previous cached SMS is never submitted.
	baseline, baselineErr := fetcher.Fetch(codeCtx, attempt.credentials.CodeURL)
	if errors.Is(baselineErr, autologincode.ErrInvalidURL) {
		s.finishAutomaticLogin(ctx, attempt, wrapError(codeAutoLoginCodeURL, ErrAccountActionRequired, nil), logger)
		return
	}
	err = s.WithAutoLoginSettingsLock(ctx, config.AccountID, func() error {
		if err := s.automaticAttemptCurrent(ctx, attempt); err != nil {
			return err
		}
		return s.startAutomaticLogin(ctx, attempt)
	})
	attempt.credentials.Password = ""
	if err == nil && attempt.verify {
		if baselineErr != nil && !errors.Is(baselineErr, autologincode.ErrNoCode) {
			err = wrapError(codeAutoLoginCodeService, ErrUpstream, nil)
		} else {
			attempt.state.Status = "waiting_code"
			if err = s.repo.(automaticLoginRepository).SaveAccountAutoLoginState(ctx, attempt.state); err == nil {
				var code string
				code, err = s.waitForAutomaticLoginCode(codeCtx, fetcher, attempt, baseline)
				if err == nil {
					err = s.WithAutoLoginSettingsLock(ctx, config.AccountID, func() error {
						if err := s.automaticAttemptCurrent(ctx, attempt); err != nil {
							return err
						}
						return s.verifyAutomaticLogin(ctx, attempt, code)
					})
					code = ""
				}
			}
		}
	}
	s.finishAutomaticLogin(ctx, attempt, err, logger)
}

func (s *Service) prepareAutomaticLogin(ctx context.Context, config domain.AccountAutoLoginConfig, channel string) (*automaticLoginAttempt, error) {
	repo := s.repo.(automaticLoginRepository)
	current, err := repo.GetAccountAutoLoginConfig(ctx, config.AccountID)
	if err != nil || !current.Enabled || current.Revision != config.Revision {
		return nil, err
	}
	account, err := s.repo.GetAccount(ctx, config.AccountID)
	if err != nil || !account.Enabled || domain.NormalizeMailboxType(account.MailboxType) != domain.MailboxTypeICloud {
		return nil, err
	}
	if !s.automaticChannelDue(ctx, current, channel) || s.hasInteractiveLogin(config.AccountID) {
		return nil, nil
	}
	state, err := repo.GetAccountAutoLoginState(ctx, config.AccountID, channel)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	now := s.now()
	next := now.Add(automaticLoginDelay(state.Failures + 1))
	state.AccountID, state.Channel, state.ConfigRevision = config.AccountID, channel, current.Revision
	state.Status, state.LastErrorCode, state.LastAttemptAt, state.NextAttemptAt = "logging_in", "", &now, &next
	state.Failures++
	if err := repo.SaveAccountAutoLoginState(ctx, state); err != nil {
		return nil, err
	}
	appleCtx, err := s.accountProxyForAccount(ctx, account)
	if err != nil {
		return nil, err
	}
	account.Proxy = cloneAccountProxy(account.Proxy)
	attempt := &automaticLoginAttempt{config: current, account: account, channel: channel, started: now, state: state, generation: s.automaticAuthGeneration(account.ID), appleCtx: appleCtx}
	attempt.credentials, err = s.cipher.(automaticLoginCipher).DecryptAccountAutoLogin(account.Email, current.CredentialsCiphertext)
	if err != nil {
		s.finishAutomaticLogin(ctx, attempt, wrapError(codeAutoLoginCredentials, ErrCredentialsInvalid, nil), nil)
		return nil, wrapError(codeAutoLoginCredentials, ErrCredentialsInvalid, nil)
	}
	attempt.appleID, err = s.automaticLoginIdentity(ctx, account)
	if err != nil {
		s.finishAutomaticLogin(ctx, attempt, err, nil)
		return nil, err
	}
	attempt.stamp, err = s.automaticSessionStamp(ctx, account.ID, channel)
	if err != nil {
		s.finishAutomaticLogin(ctx, attempt, err, nil)
		return nil, err
	}
	return attempt, nil
}

func (s *Service) hasInteractiveLogin(id int64) bool {
	s.challengeMu.Lock()
	defer s.challengeMu.Unlock()
	if flow, ok := s.accountAuthChallenges[id]; ok && flow.expires.After(s.now()) {
		return true
	}
	if key, ok := s.accountFlows[id]; ok {
		if flow, exists := s.challenges[key]; exists && flow.expiresAt.After(s.now()) {
			return true
		}
	}
	return false
}

func (s *Service) invalidateAutomaticLogin(id int64) {
	s.challengeMu.Lock()
	defer s.challengeMu.Unlock()
	if s.authGenerations == nil {
		s.authGenerations = make(map[int64]uint64)
	}
	s.authGenerations[id]++
}

func (s *Service) automaticAuthGeneration(id int64) uint64 {
	s.challengeMu.Lock()
	defer s.challengeMu.Unlock()
	return s.authGenerations[id]
}

func (s *Service) automaticLoginIdentity(ctx context.Context, account domain.Account) (string, error) {
	identity := ""
	web, err := s.repo.GetAppleWebSession(ctx, account.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if err == nil {
		identity = web.AppleID
	}
	if repo, ok := s.repo.(accountSessionRepository); ok {
		managed, err := repo.GetAppleAccountSession(ctx, account.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
		if err == nil && managed.AppleID != "" {
			if identity != "" && !sameEmail(identity, managed.AppleID) {
				return "", wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
			}
			identity = managed.AppleID
		}
	}
	if identity == "" {
		identity = account.Email
	}
	return identity, nil
}

func (s *Service) automaticSessionStamp(ctx context.Context, id int64, channel string) (string, error) {
	var value string
	if channel == "apple_account" {
		managed, err := s.readAccountManagementSession(ctx, id)
		if errors.Is(err, store.ErrNotFound) || Code(err) == CodeAccountSessionExpired {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(managed)
		if err != nil {
			return "", err
		}
		value = string(data)
	} else {
		record, err := s.repo.GetAppleWebSession(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		value = record.Ciphertext
	}
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:]), nil
}

func (s *Service) automaticAttemptCurrent(ctx context.Context, attempt *automaticLoginAttempt) error {
	config, err := s.repo.(automaticLoginRepository).GetAccountAutoLoginConfig(ctx, attempt.account.ID)
	if err != nil || !config.Enabled || config.Revision != attempt.config.Revision {
		return wrapError(codeAutoLoginChanged, ErrAccountChanged, nil)
	}
	account, err := s.repo.GetAccount(ctx, attempt.account.ID)
	if err != nil || !account.Enabled || account.MailboxConfigVersion != attempt.account.MailboxConfigVersion ||
		!sameIdentity(identityOf(account), identityOf(attempt.account)) {
		return wrapError(codeAutoLoginChanged, ErrAccountChanged, nil)
	}
	if !sameAccountProxy(account.Proxy, attempt.account.Proxy) {
		return wrapError(codeAutoLoginChanged, ErrAccountChanged, nil)
	}
	stamp, err := s.automaticSessionStamp(ctx, account.ID, attempt.channel)
	if err != nil {
		return err
	}
	if stamp != attempt.stamp || s.hasInteractiveLogin(account.ID) || s.automaticAuthGeneration(account.ID) != attempt.generation {
		return wrapError(codeAutoLoginSuperseded, ErrAccountChanged, nil)
	}
	return nil
}

func (s *Service) startAutomaticLogin(ctx context.Context, attempt *automaticLoginAttempt) error {
	var err error
	region := apple.Region(attempt.config.Region)
	if attempt.channel == "apple_account" {
		client, ok := s.client.(accountClient)
		if !ok {
			return wrapError(CodeUpstreamError, ErrUpstream, nil)
		}
		previous, _ := s.readAccountManagementSession(ctx, attempt.account.ID)
		attempt.managed, attempt.verify, err = client.SignInAccount(attempt.appleCtx, attempt.appleID, attempt.credentials.Password, region, previous)
		if err == nil && !sameEmail(attempt.managed.AppleID, attempt.appleID) {
			return wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
		}
		if err == nil && attempt.verify && attempt.managed.Verification != nil {
			if attempt.managed.Verification.Method == "" {
				return wrapError(CodePhoneSelectionRequired, ErrAccountActionRequired, nil)
			}
			if attempt.managed.Verification.DeliveryFailed {
				return mapAppleError(&apple.Error{Op: "read Account two-factor options", Kind: apple.ErrService, StatusCode: attempt.managed.Verification.DiscoveryStatus}, false)
			}
		}
	} else {
		previous := s.previousSession(ctx, attempt.account.ID, attempt.appleID, region)
		attempt.web, attempt.verify, err = s.client.SignIn(attempt.appleCtx, attempt.appleID, attempt.credentials.Password, region, previous)
		if err != nil {
			_ = s.preserveFailedWebTrust(ctx, attempt.account.ID, identityOf(attempt.account), attempt.appleID, region, attempt.web)
		}
		normalizeSession(&attempt.web, attempt.appleID, region, s.now())
		if err == nil && !sameEmail(attempt.web.AppleID, attempt.appleID) {
			return wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
		}
		if err == nil && attempt.verify && attempt.web.WebVerification != nil && attempt.web.WebVerification.Method == "" {
			return wrapError(CodePhoneSelectionRequired, ErrAccountActionRequired, nil)
		}
		if err == nil && attempt.verify && attempt.web.WebVerification != nil && attempt.web.WebVerification.DeliveryFailed {
			return mapAppleError(&apple.Error{Op: "request " + attempt.web.WebVerification.Method + " two-factor code", Kind: apple.ErrService, StatusCode: attempt.web.WebVerification.DeliveryStatus}, false)
		}
	}
	if err != nil {
		return mapAppleError(err, false)
	}
	if attempt.verify {
		return nil
	}
	return s.publishAutomaticLogin(ctx, attempt)
}

func (s *Service) verifyAutomaticLogin(ctx context.Context, attempt *automaticLoginAttempt, code string) error {
	var err error
	if attempt.channel == "apple_account" {
		attempt.managed, err = s.client.(accountClient).VerifyAccountCode(attempt.appleCtx, attempt.managed, code)
	} else {
		attempt.web, err = s.client.VerifyCode(attempt.appleCtx, attempt.web, code)
		if err != nil {
			_ = s.preserveFailedWebTrust(ctx, attempt.account.ID, identityOf(attempt.account), attempt.appleID, apple.Region(attempt.config.Region), attempt.web)
		}
	}
	if err != nil {
		return mapAppleError(err, true)
	}
	return s.publishAutomaticLogin(ctx, attempt)
}

func (s *Service) publishAutomaticLogin(ctx context.Context, attempt *automaticLoginAttempt) error {
	if err := s.automaticAttemptCurrent(ctx, attempt); err != nil {
		return err
	}
	if attempt.channel == "apple_account" {
		managed := attempt.managed
		if !sameEmail(managed.AppleID, attempt.appleID) || managed.Region != apple.Region(attempt.config.Region) || managed.APIKey == "" || managed.AuthenticatedAt.IsZero() {
			return wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
		}
		managed.RefreshRejected, managed.RefreshFailures, managed.RefreshAfter = false, 0, time.Time{}
		return s.persistAccountManagementSession(ctx, attempt.account.ID, identityOf(attempt.account), managed)
	}
	if !sameEmail(attempt.web.AppleID, attempt.appleID) || attempt.web.Region != apple.Region(attempt.config.Region) || attempt.web.SessionToken == "" {
		return wrapError(CodeAccountMismatch, ErrAccountMismatch, nil)
	}
	if err := s.attachManagementForWebLogin(ctx, attempt.account.ID, &attempt.web); err != nil {
		return err
	}
	_, err := s.persistSession(ctx, attempt.account.ID, identityOf(attempt.account), attempt.web)
	return err
}

func (s *Service) waitForAutomaticLoginCode(ctx context.Context, fetcher AutomaticLoginCodeFetcher, attempt *automaticLoginAttempt, baseline autologincode.Result) (string, error) {
	failures := 0
	for i := 0; i < 18; i++ {
		if err := s.waitAutomaticLogin(ctx, 5*time.Second); err != nil {
			return "", err
		}
		if err := s.automaticAttemptCurrent(ctx, attempt); err != nil {
			return "", err
		}
		result, err := fetcher.Fetch(ctx, attempt.credentials.CodeURL)
		if err != nil && !errors.Is(err, autologincode.ErrNoCode) {
			failures++
			if failures >= 3 {
				return "", wrapError(codeAutoLoginCodeService, ErrUpstream, nil)
			}
			continue
		}
		failures = 0
		if err == nil && automaticCodeFresh(result, baseline, attempt.started, s.now()) {
			return result.Code, nil
		}
	}
	return "", wrapError(codeAutoLoginCodeTimeout, ErrUpstream, nil)
}

func automaticCodeFresh(result, baseline autologincode.Result, started, now time.Time) bool {
	if len(result.Code) != 6 {
		return false
	}
	for _, digit := range result.Code {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	if result.ReceivedAt != nil && result.ReceivedAt.Before(started.Add(-30*time.Second)) {
		return false
	}
	if result.ReceivedAt != nil && result.ReceivedAt.After(now.Add(30*time.Second)) {
		return false
	}
	if result.ReceivedAt != nil && baseline.ReceivedAt != nil && !result.ReceivedAt.After(*baseline.ReceivedAt) {
		return false
	}
	if baseline.Code != "" && result.Code == baseline.Code {
		return false
	}
	return true
}

func (s *Service) finishAutomaticLogin(ctx context.Context, attempt *automaticLoginAttempt, result error, logger *slog.Logger) {
	if ctx.Err() != nil {
		return // The reserved retry deadline remains durable after interruption.
	}
	state := attempt.state
	if result == nil {
		now := s.now()
		state.Status, state.Failures, state.LastErrorCode, state.LastSuccessAt, state.NextAttemptAt = "authenticated", 0, "", &now, nil
	} else {
		state.Status, state.LastErrorCode = "retry_wait", Code(result)
		if state.LastErrorCode == "" {
			state.LastErrorCode = CodeUpstreamError
		}
		next := s.now().Add(max(automaticLoginDelay(state.Failures), apple.RetryDelay(result)))
		state.NextAttemptAt = &next
		if errors.Is(result, ErrCredentialsInvalid) || errors.Is(result, ErrAccountActionRequired) || errors.Is(result, ErrAccountMismatch) {
			state.Status, state.NextAttemptAt = "needs_action", nil
		}
	}
	if err := s.repo.(automaticLoginRepository).SaveAccountAutoLoginState(ctx, state); err != nil {
		return
	}
	if logger != nil {
		message := "Apple 通道自动重新登录暂未完成"
		if result == nil {
			message = "Apple 通道已自动重新登录"
		}
		attrs := []any{"operation", "apple_auto_login", "account_id", attempt.account.ID,
			"channel", attempt.channel, "status", state.Status, "error_code", state.LastErrorCode}
		var upstream *apple.Error
		if errors.As(result, &upstream) {
			attrs = append(attrs, "upstream_operation", upstream.Op, "http_status", upstream.StatusCode,
				"service_code", upstream.ServiceCode)
		}
		logger.Info(message, attrs...)
	}
}
