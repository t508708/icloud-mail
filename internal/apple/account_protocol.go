package apple

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Protocol reference: q1953258942/iCloud-Privacy-Mail, commit 3a839c7a.
// Account management and iCloud Web use distinct widgets and cookie state.
const accountWidget = "af1139274f266b22b68c2a3e7ad932cb3c0bbe854e13a79af78dcc73136882c3"
const accountPortal = "https://account.apple.com"
const accountManage = "https://appleid.apple.com"
const accountAuth = "https://idmsa.apple.com/appleauth/auth"
const accountUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36"

type AccountSession struct {
	AppleID              string             `json:"apple_id"`
	Region               Region             `json:"region"`
	SCNT                 string             `json:"scnt,omitempty"`
	APIKey               string             `json:"api_key,omitempty"`
	SessionToken         string             `json:"session_token,omitempty"`
	SessionID            string             `json:"session_id,omitempty"`
	TrustToken           string             `json:"trust_token,omitempty"`
	AuthAttributes       string             `json:"auth_attributes,omitempty"`
	FrameID              string             `json:"frame_id,omitempty"`
	AuthenticatedAt      time.Time          `json:"authenticated_at,omitempty"`
	ExpiresAt            time.Time          `json:"expires_at,omitempty"`
	UpdatedAt            time.Time          `json:"updated_at,omitempty"`
	RefreshedAt          time.Time          `json:"refreshed_at,omitempty"`
	RefreshAfter         time.Time          `json:"refresh_after,omitempty"`
	RefreshFailures      int                `json:"refresh_failures,omitempty"`
	RefreshRejected      bool               `json:"refresh_rejected,omitempty"`
	RenewalJitterSeconds int                `json:"renewal_jitter_seconds,omitempty"`
	Cookies              []PersistentCookie `json:"cookies,omitempty"`
}

// Keep the reference client's four-minute cadence even when the management
// TTL is longer. The TTL is not a lifetime guarantee for the whole login.
func (s AccountSession) NextRefreshAt() time.Time {
	anchor := s.RefreshedAt
	if anchor.IsZero() {
		anchor = s.AuthenticatedAt
	}
	var next time.Time
	if !anchor.IsZero() {
		interval := 4 * time.Minute
		if s.RenewalJitterSeconds >= 240 && s.RenewalJitterSeconds <= 360 {
			interval = time.Duration(s.RenewalJitterSeconds) * time.Second
		}
		next = anchor.Add(interval)
	}
	if !s.ExpiresAt.IsZero() {
		lead := 3 * time.Minute
		if lifetime := s.ExpiresAt.Sub(anchor); lifetime > 0 && lifetime/5 < lead {
			lead = lifetime / 5
		}
		if deadline := s.ExpiresAt.Add(-lead); next.IsZero() || deadline.Before(next) {
			next = deadline
		}
	}
	if s.RefreshAfter.After(next) {
		next = s.RefreshAfter
	}
	return next
}

// Refresh while creation waits on its own quota, but honor upstream backoff
// and stop after explicit rejection of the saved login.
func (s AccountSession) NeedsRefresh(now time.Time) bool {
	return !s.RefreshRejected && !s.NextRefreshAt().After(now)
}

func accountHeaders(s AccountSession, auth bool) http.Header {
	h := make(http.Header)
	h.Set("Accept", "application/json, text/plain, */*")
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", accountUserAgent)
	h.Set("Accept-Language", "zh,en;q=0.9")
	h.Set("Sec-CH-UA", `"Google Chrome";v="149", "Chromium";v="149", "Not)A;Brand";v="24"`)
	h.Set("Sec-CH-UA-Mobile", "?0")
	h.Set("Sec-CH-UA-Platform", `"macOS"`)
	h.Set("Sec-Fetch-Dest", "empty")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Site", "same-origin")
	h.Set("Origin", accountPortal)
	h.Set("Referer", accountPortal+"/")
	fd, _ := json.Marshal(map[string]string{"U": accountUserAgent, "L": "zh", "Z": "GMT+08:00", "V": "1.1", "F": accountFingerprint(time.Now())})
	h.Set("X-Apple-I-FD-Client-Info", string(fd))
	if s.SCNT != "" {
		h.Set("scnt", s.SCNT)
	}
	if auth {
		frame := "auth-" + s.FrameID
		h.Set("Origin", "https://idmsa.apple.com")
		h.Set("Referer", "https://idmsa.apple.com/")
		h.Set("Accept", "application/json, text/javascript, */*; q=0.01")
		h.Set("X-Apple-Widget-Key", accountWidget)
		h.Set("X-Apple-OAuth-Client-Id", accountWidget)
		h.Set("X-Apple-OAuth-Client-Type", "firstPartyAuth")
		h.Set("X-Apple-OAuth-Redirect-URI", accountPortal)
		h.Set("X-Apple-OAuth-Response-Mode", "web_message")
		h.Set("X-Apple-OAuth-Response-Type", "code")
		h.Set("X-Apple-OAuth-State", frame)
		h.Set("X-Apple-Frame-Id", frame)
		h.Set("X-Requested-With", "XMLHttpRequest")
		h.Set("X-Apple-Domain-Id", "11")
		h.Set("X-Apple-Privacy-Consent", "true")
		h.Set("X-Apple-Privacy-Consent-Accepted", "true")
		if s.SessionID != "" {
			h.Set("X-Apple-ID-Session-Id", s.SessionID)
		}
		if s.SessionToken != "" {
			h.Set("X-Apple-Session-Token", s.SessionToken)
		}
		if s.AuthAttributes != "" {
			h.Set("X-Apple-Auth-Attributes", s.AuthAttributes)
		}
	} else {
		h.Set("X-Apple-I-Request-Context", "ca")
		h.Set("X-Apple-I-TimeZone", "Asia/Shanghai")
		if s.APIKey != "" {
			h.Set("X-Apple-Api-Key", s.APIKey)
		}
	}
	return h
}

// Failed management responses do not overwrite a usable SCNT or cookie jar.
// Redirects never forward authentication material to a different endpoint.
func (c *Client) accountCall(ctx context.Context, s *AccountSession, method, rawURL string, payload any, headers http.Header, allowConflict bool) (responseData, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Host != "idmsa.apple.com" && u.Host != "appleid.apple.com" && u.Host != "account.apple.com") {
		return responseData{}, operationError("account URL", ErrInvalidConfig, 0, nil)
	}
	jar, err := NewPersistentJar(s.Cookies)
	if err != nil {
		return responseData{}, err
	}
	var body io.Reader
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return responseData{}, e
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return responseData{}, err
	}
	req.Header = headers.Clone()
	client := &http.Client{Transport: c.transport, Timeout: c.timeout, Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return responseData{}, operationError("account transport", ErrService, 0, err)
	}
	defer resp.Body.Close()
	r := responseData{status: resp.StatusCode, header: resp.Header.Clone()}
	r.body, err = io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
	if err != nil {
		return r, r.operationError("account response", ErrInvalidResponse, err)
	}
	if int64(len(r.body)) > c.maxResponseBytes {
		return r, r.operationError("account response", ErrResponseTooLarge, nil)
	}
	accepted := r.status >= 200 && r.status < 300 || allowConflict && r.status == 409
	if !accepted {
		kind := ErrService
		if r.status == 419 || r.status == 401 || r.status == 403 {
			kind = ErrInvalidSession
		}
		if r.status == 412 {
			kind = ErrTermsRequired
		}
		return r, accountResponseError(r, kind)
	}
	if r.status != 409 {
		var envelope struct {
			ErrorCode     json.RawMessage   `json:"errorCode"`
			ServiceErrors []json.RawMessage `json:"serviceErrors"`
		}
		code := ""
		if json.Unmarshal(r.body, &envelope) == nil {
			code = strings.Trim(string(envelope.ErrorCode), `"`)
		}
		if code != "" && code != "null" && code != "0" || len(envelope.ServiceErrors) > 0 {
			return r, accountResponseError(r, ErrService)
		}
	}
	s.Cookies = jar.Export()
	s.UpdatedAt = time.Now().UTC()
	for key, target := range map[string]*string{"scnt": &s.SCNT, "X-Apple-ID-Session-Id": &s.SessionID, "X-Apple-Session-Token": &s.SessionToken, "X-Apple-Auth-Attributes": &s.AuthAttributes, "X-Apple-TwoSV-Trust-Token": &s.TrustToken} {
		if value := r.header.Get(key); value != "" {
			*target = value
		}
	}
	return r, nil
}

func accountResponseError(r responseData, kind error) *Error {
	e := r.operationError("account API", kind, nil)
	var envelope struct {
		ErrorCode           json.RawMessage `json:"errorCode"`
		Code                json.RawMessage `json:"code"`
		ErrorMessage        string          `json:"errorMessage"`
		IneligibilityReason string          `json:"ineligibilityReason"`
		ServiceErrors       []struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
		} `json:"serviceErrors"`
	}
	if json.Unmarshal(r.body, &envelope) == nil {
		code := envelope.ErrorCode
		if len(code) == 0 {
			code = envelope.Code
		}
		if len(code) == 0 && len(envelope.ServiceErrors) > 0 {
			code = envelope.ServiceErrors[0].Code
		}
		value := strings.Trim(string(code), `"`)
		if _, err := strconv.ParseInt(value, 10, 64); err == nil {
			e.ServiceCode = value
		}
		message := strings.ToLower(envelope.ErrorMessage)
		for _, item := range envelope.ServiceErrors {
			message += " " + strings.ToLower(item.Message)
		}
		if strings.Contains(message, "limit of addresses") || strings.Contains(message, "maximum number of") || strings.Contains(message, "too many") {
			e.ServiceCode = hmeRateLimitCodeBatch
		}
		if strings.EqualFold(strings.TrimSpace(envelope.IneligibilityReason), "rate_limit_exceeded") {
			// This explicit settings response is a throttle, including on HTTP
			// 412. Do not expose it as an account-terms action to service callers.
			e.ServiceCode = hmeRateLimitCodeBatch
			e.Kind = ErrService
		}
	}
	return e
}

func (c *Client) accountManagementCall(ctx context.Context, s *AccountSession, method, path string, body any) (responseData, error) {
	h := accountHeaders(*s, false)
	if path == "/account/manage/gs/ws/token" || path == "/account/manage" {
		h.Del("X-Apple-Api-Key")
	}
	r, err := c.accountCall(ctx, s, method, accountManage+path, body, h, false)
	var upstream *Error
	if errors.As(err, &upstream) {
		upstream.Op = "account " + method + " " + path
	}
	return r, err
}

func (c *Client) warmAccountPortal(ctx context.Context, s *AccountSession) error {
	h := accountHeaders(*s, false)
	h.Del("X-Apple-Api-Key")
	h.Del("scnt")
	// A document navigation is not a JSON request; the portal returns HTTP
	// 500 when application/json is sent here even though the GET has no body.
	h.Del("Content-Type")
	h.Set("Accept", "text/html,application/xhtml+xml")
	h.Set("Sec-Fetch-Dest", "document")
	h.Set("Sec-Fetch-Mode", "navigate")
	if _, err := c.accountCall(ctx, s, http.MethodGet, accountPortal+"/account/manage/section/privacy", nil, h, false); err != nil {
		return err
	}
	h = accountHeaders(*s, false)
	h.Del("X-Apple-Api-Key")
	r, err := c.accountCall(ctx, s, http.MethodGet, accountPortal+"/bootstrap/portal", nil, h, false)
	if err == nil {
		setAccountTTL(s, r.body)
	}
	return err
}

func setAccountTTL(s *AccountSession, body []byte) {
	var token struct {
		Minutes int `json:"timeOutInterval"`
	}
	if json.Unmarshal(body, &token) == nil && token.Minutes > 0 && token.Minutes <= 24*60 {
		s.ExpiresAt = time.Now().UTC().Add(time.Duration(token.Minutes) * time.Minute)
	}
}

func (c *Client) RefreshAccountSession(ctx context.Context, s AccountSession) (AccountSession, error) {
	if s.SCNT == "" {
		return s, operationError("account session", ErrInvalidSession, 0, nil)
	}
	refresh := func(withoutSCNT bool) error {
		tokenSession := s
		if withoutSCNT {
			tokenSession.SCNT = ""
		}
		r, err := c.accountManagementCall(ctx, &tokenSession, http.MethodGet, "/account/manage/gs/ws/token", nil)
		if err != nil {
			return err
		}
		if tokenSession.SCNT == "" {
			return operationError("account management token", ErrInvalidSession, r.status, nil)
		}
		s = tokenSession
		setAccountTTL(&s, r.body)
		r, err = c.accountManagementCall(ctx, &s, http.MethodGet, "/account/manage", nil)
		if err != nil {
			return err
		}
		var info struct {
			APIKey string `json:"apiKey"`
		}
		if json.Unmarshal(r.body, &info) != nil || strings.TrimSpace(info.APIKey) == "" {
			return operationError("account API key", ErrInvalidSession, r.status, nil)
		}
		s.APIKey = info.APIKey
		// Touch the management resource after rotating the token/API key. This
		// mirrors the reference keepalive flow and extends the idle management
		// session instead of merely fetching a local TTL value.
		if _, err := c.accountManagementCall(ctx, &s, http.MethodGet, "/account/manage/forwardemail", nil); err != nil {
			return err
		}
		if s.AuthenticatedAt.IsZero() {
			s.AuthenticatedAt = time.Now().UTC()
		}
		s.RefreshedAt = time.Now().UTC()
		s.RefreshAfter = time.Time{}
		s.RefreshFailures = 0
		s.RefreshRejected = false
		s.RenewalJitterSeconds = randomRenewalJitterSeconds()
		return nil
	}
	err := refresh(false)
	if err != nil && errors.Is(err, ErrInvalidSession) {
		if warmErr := c.warmAccountPortal(ctx, &s); warmErr != nil {
			return s, warmErr
		}
		// The reference recovery bootstraps a token from existing cookies
		// without a stale SCNT, then falls back to the accepted checkpoint.
		err = refresh(true)
		if errors.Is(err, ErrInvalidSession) {
			err = refresh(false)
		}
	}
	return s, err
}

func randomRenewalJitterSeconds() int {
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(121))
	if err != nil {
		return 300
	}
	return 240 + int(value.Int64())
}

func (c *Client) SignInAccount(ctx context.Context, appleID, password string, region Region, previous *AccountSession) (AccountSession, bool, error) {
	if region == "" {
		region = RegionGlobal
	}
	s := AccountSession{AppleID: strings.ToLower(strings.TrimSpace(appleID)), Region: region}
	if s.AppleID == "" || password == "" {
		return s, false, operationError("account sign in", ErrAuthentication, 0, nil)
	}
	if region != RegionGlobal && region != RegionChina {
		return s, false, ErrInvalidConfig
	}
	// The reference uses global account management for both mail regions.
	if previous != nil && strings.EqualFold(previous.AppleID, s.AppleID) && previous.Region == region {
		s.Cookies = append([]PersistentCookie(nil), previous.Cookies...)
		s.TrustToken = previous.TrustToken
	}
	frame, err := c.newUUID()
	if err != nil {
		return s, false, err
	}
	s.FrameID = frame
	if err = c.warmAccountPortal(ctx, &s); err != nil {
		return s, false, err
	}
	h := accountHeaders(s, false)
	h.Del("scnt")
	h.Del("X-Apple-Api-Key")
	r, tokenErr := c.accountCall(ctx, &s, http.MethodGet, accountManage+"/account/manage/gs/ws/token", nil, h, false)
	manageSCNT := r.header.Get("scnt")
	if tokenErr != nil && manageSCNT == "" {
		return s, false, tokenErr
	}
	q := url.Values{"frame_id": {"auth-" + frame}, "skVersion": {"7"}, "iframeId": {"auth-" + frame}, "client_id": {accountWidget}, "redirect_uri": {accountPortal}, "response_type": {"code"}, "response_mode": {"web_message"}, "state": {"auth-" + frame}, "authVersion": {"8.0.2"}}
	h = accountHeaders(s, true)
	h.Set("Accept", "text/html,application/xhtml+xml")
	h.Del("Content-Type")
	h.Set("Referer", accountPortal+"/")
	h.Set("Sec-Fetch-Dest", "iframe")
	h.Set("Sec-Fetch-Mode", "navigate")
	h.Set("Sec-Fetch-Site", "same-site")
	r, err = c.accountCall(ctx, &s, http.MethodGet, accountAuth+"/authorize/signin?"+q.Encode(), nil, h, false)
	if err != nil {
		return s, false, err
	}
	bits, _ := strconv.Atoi(r.header.Get("X-Apple-HC-Bits"))
	challenge := r.header.Get("X-Apple-HC-Challenge")
	h = accountHeaders(s, true)
	h.Del("scnt")
	h.Del("X-Apple-ID-Session-Id")
	if _, err = c.accountCall(ctx, &s, http.MethodPost, accountAuth+"/verify/device/key/challenge", map[string]bool{"passkeyAutofill": false}, h, false); err != nil {
		return s, false, err
	}
	if _, err = c.accountCall(ctx, &s, http.MethodPost, accountAuth+"/federate?isRememberMeEnabled=true", map[string]any{"accountName": s.AppleID, "rememberMe": true}, accountHeaders(s, true), false); err != nil {
		return s, false, err
	}
	secret := make([]byte, 32)
	if err = c.readRandom(secret); err != nil {
		return s, false, err
	}
	srp, err := newSRPClient(bytes.NewReader(secret))
	if err != nil {
		return s, false, err
	}
	r, err = c.accountCall(ctx, &s, http.MethodPost, accountAuth+"/signin/init", map[string]any{"a": base64.StdEncoding.EncodeToString(srp.publicKey()), "accountName": s.AppleID, "protocols": []string{"s2k", "s2k_fo"}}, accountHeaders(s, true), false)
	if err != nil {
		return s, false, err
	}
	var init struct {
		Salt      string `json:"salt"`
		B         string `json:"b"`
		C         string `json:"c"`
		Iteration int    `json:"iteration"`
		Protocol  string `json:"protocol"`
	}
	if json.Unmarshal(r.body, &init) != nil || init.C == "" {
		return s, false, ErrInvalidResponse
	}
	salt, err := base64.StdEncoding.Strict().DecodeString(init.Salt)
	if err != nil || len(salt) == 0 || len(salt) > 1024 {
		return s, false, ErrInvalidResponse
	}
	server, err := base64.StdEncoding.Strict().DecodeString(init.B)
	if err != nil || len(server) == 0 || len(server) > appleSRPSize {
		return s, false, ErrInvalidResponse
	}
	key, err := deriveApplePassword(password, salt, init.Iteration, init.Protocol)
	password = ""
	if err != nil {
		return s, false, err
	}
	if err = srp.processChallenge([]byte(s.AppleID), key, salt, server); err != nil {
		return s, false, err
	}
	if bits == 0 {
		bits, _ = strconv.Atoi(r.header.Get("X-Apple-HC-Bits"))
		challenge = r.header.Get("X-Apple-HC-Challenge")
	}
	hc, err := accountHashcash(ctx, bits, challenge)
	if err != nil {
		return s, false, err
	}
	h = accountHeaders(s, true)
	h.Set("X-Apple-HC", hc)
	trustTokens := []string{}
	if s.TrustToken != "" {
		trustTokens = append(trustTokens, s.TrustToken)
	}
	r, err = c.accountCall(ctx, &s, http.MethodPost, accountAuth+"/signin/complete?isRememberMeEnabled=true", map[string]any{"accountName": s.AppleID, "m1": base64.StdEncoding.EncodeToString(srp.m1), "m2": base64.StdEncoding.EncodeToString(srp.m2), "c": init.C, "rememberMe": true, "trustTokens": trustTokens}, h, true)
	if err != nil {
		if r.status == 401 || r.status == 403 {
			return s, false, accountResponseError(r, ErrAuthentication)
		}
		return s, false, err
	}
	if r.status == 409 {
		h = accountHeaders(s, true)
		h.Set("Accept", "text/html")
		h.Del("Origin")
		h.Del("Content-Type")
		_, _ = c.accountCall(ctx, &s, http.MethodGet, accountAuth, nil, h, false)
		return s, true, nil
	}
	if s.SCNT == "" {
		s.SCNT = manageSCNT
	}
	s, err = c.RefreshAccountSession(ctx, s)
	return s, false, err
}

func (c *Client) VerifyAccountCode(ctx context.Context, s AccountSession, code string) (AccountSession, error) {
	if !validSecurityCode(code) {
		return s, ErrTwoFactorCode
	}
	if s.SCNT == "" || s.FrameID == "" {
		return s, ErrInvalidSession
	}
	h := accountHeaders(s, true)
	h.Set("Accept", "application/json, text/plain, */*")
	h.Set("X-Apple-App-Id", accountWidget)
	h.Del("X-Requested-With")
	r, err := c.accountCall(ctx, &s, http.MethodPost, accountAuth+"/verify/trusteddevice/securitycode", map[string]any{"securityCode": map[string]string{"code": code}}, h, false)
	if err != nil {
		if r.status == 400 || r.status == 401 || r.status == 403 || r.status == 409 || r.status == 422 {
			return s, accountResponseError(r, ErrTwoFactorCode)
		}
		return s, err
	}
	response, trustErr := c.accountCall(ctx, &s, http.MethodGet, accountAuth+"/2sv/trust", nil, accountHeaders(s, true), false)
	if trustErr != nil {
		return s, &Error{Op: "trust Apple Account session", Kind: ErrService, StatusCode: response.status, Retryable: response.status >= 500}
	}
	return c.RefreshAccountSession(ctx, s)
}

func (c *Client) CreateAccountAlias(ctx context.Context, s AccountSession, label, note string) (Alias, AccountSession, error) {
	if s.APIKey == "" || s.SCNT == "" || s.RefreshRejected {
		return Alias{}, s, ErrInvalidSession
	}
	now := time.Now()
	if s.RefreshAfter.After(now) && !now.Before(s.ExpiresAt) {
		return Alias{}, s, &Error{Op: "account refresh backoff", Kind: ErrService, StatusCode: http.StatusServiceUnavailable, Retryable: true, RetryAfter: s.RefreshAfter.Sub(now)}
	}
	if s.NeedsRefresh(now) || !now.Before(s.ExpiresAt) {
		var err error
		s, err = c.RefreshAccountSession(ctx, s)
		if err != nil {
			return Alias{}, s, err
		}
	}
	r, err := c.accountManagementCall(ctx, &s, http.MethodPost, "/account/manage/email/private/add", map[string]any{})
	if r.status == http.StatusUnauthorized && errors.Is(err, ErrInvalidSession) {
		// Only an explicit 401 before a candidate exists permits one retry.
		// Never replay a timeout, an ambiguous result, or completion.
		s, err = c.RefreshAccountSession(ctx, s)
		if err == nil {
			r, err = c.accountManagementCall(ctx, &s, http.MethodPost, "/account/manage/email/private/add", map[string]any{})
		}
	}
	if err != nil {
		return Alias{}, s, err
	}
	var candidate struct {
		EmailAddress string `json:"emailAddress"`
	}
	if json.Unmarshal(r.body, &candidate) != nil || !validAccountAliasAddress(candidate.EmailAddress) {
		return Alias{}, s, r.operationError("account generate response", ErrInvalidResponse, nil)
	}
	candidate.EmailAddress = strings.TrimSpace(candidate.EmailAddress)
	alias := Alias{HME: strings.ToLower(candidate.EmailAddress), Label: label, Note: note, Origin: "APPLE_ACCOUNT"}
	r, err = c.accountManagementCall(ctx, &s, http.MethodPut, "/account/manage/email/private/add/complete", map[string]string{"emailAddress": candidate.EmailAddress, "label": label, "note": note})
	if err != nil {
		// Only explicit rejection clears the candidate. Ambiguous completion
		// retains it for durable staging and read-only directory confirmation.
		if IsRateLimited(err) || errors.Is(err, ErrInvalidSession) {
			return Alias{}, s, err
		}
		return alias, s, err
	}
	var completed struct {
		EmailAddress string `json:"emailAddress"`
		ID           string `json:"id"`
		Active       bool   `json:"active"`
	}
	if json.Unmarshal(r.body, &completed) != nil || completed.ID == "" || completed.EmailAddress != "" && !strings.EqualFold(completed.EmailAddress, candidate.EmailAddress) {
		return alias, s, r.operationError("account complete response", ErrInvalidResponse, nil)
	}
	alias.AnonymousID = completed.ID
	alias.IsActive = completed.Active
	// The shared Web directory path confirms activation and forwarding.
	return alias, s, nil
}

func validAccountAliasAddress(address string) bool {
	address = strings.TrimSpace(address)
	parts := strings.Split(address, "@")
	return len(parts) == 2 && parts[0] != "" && strings.EqualFold(parts[1], "icloud.com") && !strings.ContainsAny(address, " \r\n\t<>")
}

func accountHashcash(ctx context.Context, bits int, challenge string) (string, error) {
	if bits < 1 || bits > 24 || challenge == "" || len(challenge) > 1024 {
		return "", operationError("account proof challenge", ErrInvalidResponse, 0, nil)
	}
	prefix := fmt.Sprintf("1:%d:%s:%s::", bits, time.Now().UTC().Format("20060102150405"), challenge)
	for n := uint64(0); n < 1<<27; n++ {
		if n%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return "", err
			}
		}
		proof := prefix + strconv.FormatUint(n, 36)
		digest := sha1.Sum([]byte(proof))
		zeros := 0
		for _, b := range digest {
			if b == 0 {
				zeros += 8
				continue
			}
			for mask := byte(128); b&mask == 0; mask >>= 1 {
				zeros++
			}
			break
		}
		if zeros >= bits {
			return proof, nil
		}
	}
	return "", operationError("account proof exhausted", ErrService, 0, nil)
}
