package httpserver

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"icloud-api/internal/domain"
	"icloud-api/internal/secure"
	"icloud-api/internal/store"
)

type otpResponse struct {
	OTP  string `json:"otp"`
	Time string `json:"time"`
}

type otpRequestContext struct {
	Binding      domain.MailboxBinding
	ProjectID    string
	PickupScope  string
	Filter       store.OTPQueryFilter
	ReturnLatest bool
	WaitDuration time.Duration
}

// otpHistory is deliberately a repeatable cache read. Neither authentication
// form consumes a code or changes upstream/local message flags.
func (s *Server) otpHistory(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	reqCtx, ok := s.authenticateOTPRequest(c)
	if !ok {
		return
	}
	binding := reqCtx.Binding
	// OTP history is part of the v2 contract. Legacy aliases must continue to
	// use the original mailbox endpoints even if a stale credential bundle or
	// hash happens to remain on the row.
	if binding.Alias.CredentialMode != domain.AliasCredentialModeV2 ||
		!binding.Alias.Enabled || !binding.Account.Enabled {
		s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "API Key 无效")
		return
	}
	waiting := reqCtx.WaitDuration > 0 && s.sharedMailboxReceiver && s.mailboxChanges != nil
	release, ok := s.beginScopedMailboxPickupMode(c, binding.Account.ID, binding.Alias.ID, reqCtx.PickupScope, waiting)
	if !ok {
		return
	}
	defer release()

	waitCtx := c.Request.Context()
	if reqCtx.WaitDuration > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(waitCtx, reqCtx.WaitDuration)
		defer cancel()
	}

	if reqCtx.WaitDuration > 0 && s.demandAliasSync != nil && s.mailboxChanges != nil {
		if err := s.demandAliasSync(waitCtx, binding.Alias.ID); err != nil {
			if c.Request.Context().Err() != nil {
				return
			}
			if waitCtx.Err() == nil {
				c.Header("Retry-After", "2")
				s.writeAPIError(c, http.StatusServiceUnavailable, "SYNC_UNAVAILABLE", "本次按需取件尚未完成，请稍后刷新取件地址")
				return
			}
		}
		for {
			if c.Request.Context().Err() != nil {
				return
			}
			var changed <-chan struct{}
			var nextCheck time.Duration
			if waitCtx.Err() == nil {
				changed, nextCheck = s.mailboxChanges(binding.Account.ID)
			}
			if s.writeOTPHistoryIfReady(c, reqCtx, changed == nil) {
				return
			}
			timer := time.NewTimer(max(nextCheck, 50*time.Millisecond))
			select {
			case <-waitCtx.Done():
				timer.Stop()
				if c.Request.Context().Err() == nil {
					s.writeOTPHistoryIfReady(c, reqCtx, true)
				}
				return
			case <-changed:
			case <-timer.C:
			}
			timer.Stop()
			if waitCtx.Err() != nil {
				continue
			}
			if err := s.demandAliasSync(waitCtx, binding.Alias.ID); err != nil {
				if waitCtx.Err() != nil && c.Request.Context().Err() == nil {
					s.writeOTPHistoryIfReady(c, reqCtx, true)
				} else if c.Request.Context().Err() == nil {
					c.Header("Retry-After", "2")
					s.writeAPIError(c, http.StatusServiceUnavailable, "SYNC_UNAVAILABLE", "本次按需取件尚未完成，请稍后刷新取件地址")
				}
				return
			}
		}
	}

	binding, ok = s.refreshDemandMailbox(c, binding)
	if !ok {
		return
	}
	reqCtx.Binding = binding
	s.writeOTPHistoryIfReady(c, reqCtx, true)
}

func (s *Server) writeOTPHistoryIfReady(c *gin.Context, reqCtx otpRequestContext, final bool) bool {
	if reqCtx.ProjectID != "" {
		if _, err := s.store.VerifyPoolProjectOTPAccess(c.Request.Context(), reqCtx.ProjectID, reqCtx.Binding.Alias.ID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "项目取码凭据已失效或已解除共享")
			} else {
				s.logger.Error("复核项目验证码授权失败", "error", err, "request_id", requestID(c))
				s.writeAPIError(c, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "数据库暂不可用")
			}
			return true
		}
	}
	current, err := s.store.GetMailboxBindingByAPIKeyHash(c.Request.Context(), reqCtx.Binding.Alias.APIKeyHash)
	if err != nil || !current.Alias.Enabled || !current.Account.Enabled ||
		current.Alias.ID != reqCtx.Binding.Alias.ID ||
		!secure.HashEqual(current.Alias.APIKeyHash, reqCtx.Binding.Alias.APIKeyHash) {
		s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "邮箱凭据已更新或邮箱已停用")
		return true
	}

	records, err := s.store.ListAliasOTPsFiltered(c.Request.Context(), current.Alias.ID, current.Alias.Address, reqCtx.Filter)
	if err != nil {
		s.logger.Error("读取验证码归档失败", "alias_id", current.Alias.ID, "error", err, "request_id", requestID(c))
		s.writeAPIError(c, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "数据库暂不可用")
		return true
	}
	if len(records) == 0 && !final {
		return false
	}

	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	if touchErr := s.store.TouchAliasAccess(c.Request.Context(), current.Alias.ID, now); touchErr != nil {
		s.logger.Warn("更新 API 最近访问时间失败", "alias_id", current.Alias.ID, "error", touchErr, "request_id", requestID(c))
	}

	location := s.cfg.Timezone
	if location == nil {
		location = time.Local
	}
	result := make([]otpResponse, 0, len(records))
	for _, record := range records {
		result = append(result, otpResponse{OTP: record.OTP, Time: record.Time.In(location).Format(time.RFC3339)})
	}
	if (s.cfg.OTPReturnLatestOnly || reqCtx.ReturnLatest) && len(result) > 0 {
		c.JSON(http.StatusOK, result[0])
		return true
	}
	c.JSON(http.StatusOK, result)
	return true
}

func (s *Server) authenticateOTPRequest(c *gin.Context) (otpRequestContext, bool) {
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	values, queryErr := url.ParseQuery(c.Request.URL.RawQuery)
	queryTokens := values["token"]
	queryPresent := len(queryTokens) > 0
	if queryErr != nil || len(queryTokens) > 1 || (authorization != "" && queryPresent) {
		s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "API Key 无效")
		return otpRequestContext{}, false
	}

	var (
		binding     domain.MailboxBinding
		project     store.PoolClient
		projectID   string
		pickupScope string
		err         error
	)
	if authorization != "" {
		scheme, token, found := strings.Cut(authorization, " ")
		if !found || !strings.EqualFold(scheme, "Bearer") || !validAPIKey(token) || strings.Contains(token, " ") {
			s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "API Key 无效")
			return otpRequestContext{}, false
		}
		binding, err = s.store.GetMailboxBindingByAPIKeyHash(c.Request.Context(), secure.HashToken(token))
	} else if len(queryTokens) == 1 {
		token := queryTokens[0]
		if aliasID, clientID, isProjectToken := secure.ProjectOTPTokenIdentity(token); isProjectToken {
			alias, lookupErr := s.store.GetAlias(c.Request.Context(), aliasID)
			switch {
			case lookupErr != nil:
				err = lookupErr
			case !s.cipher.VerifyProjectOTPToken(token, aliasID, alias.APIKeyHash, clientID):
				err = store.ErrNotFound
			default:
				project, err = s.store.VerifyPoolProjectOTPAccess(c.Request.Context(), clientID, aliasID)
				if err == nil {
					projectID = project.ID
					pickupScope = "proj:" + project.ID
					binding, err = s.store.GetMailboxBindingByAPIKeyHash(c.Request.Context(), alias.APIKeyHash)
				}
			}
		} else if validAPIKey(token) {
			aliasID, candidate := secure.OTPTokenAliasID(token)
			if !candidate {
				err = store.ErrNotFound
			} else {
				alias, lookupErr := s.store.GetAlias(c.Request.Context(), aliasID)
				switch {
				case lookupErr != nil:
					err = lookupErr
				case !s.cipher.VerifyOTPToken(token, aliasID, alias.APIKeyHash):
					err = store.ErrNotFound
				default:
					binding, err = s.store.GetMailboxBindingByAPIKeyHash(c.Request.Context(), alias.APIKeyHash)
				}
			}
		} else {
			err = store.ErrNotFound
		}
	} else {
		err = store.ErrNotFound
	}
	if errors.Is(err, store.ErrNotFound) {
		s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "API Key 无效")
		return otpRequestContext{}, false
	}
	if err != nil {
		s.logger.Error("查询验证码凭证绑定失败", "error", err, "request_id", requestID(c))
		s.writeAPIError(c, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "数据库暂不可用")
		return otpRequestContext{}, false
	}

	reqCtx := otpRequestContext{
		Binding:     binding,
		ProjectID:   projectID,
		PickupScope: pickupScope,
		Filter: store.OTPQueryFilter{
			SenderFilter:  project.SenderFilter,
			SubjectFilter: project.SubjectFilter,
			Limit:         100,
		},
	}
	if !s.parseOTPQueryModifiers(c, values, binding.Alias.Address, &reqCtx) {
		return otpRequestContext{}, false
	}
	c.Set(bindingKey, binding)
	return reqCtx, true
}

func (s *Server) parseOTPQueryModifiers(
	c *gin.Context,
	values url.Values,
	rootAddress string,
	reqCtx *otpRequestContext,
) bool {
	for key, list := range values {
		if key == "token" {
			continue
		}
		if len(list) > 1 {
			s.writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "查询参数不能重复传递: "+key)
			return false
		}
		raw := strings.TrimSpace(list[0])
		switch key {
		case "after":
			if raw != "" {
				parsed, parseErr := parseFlexibleRFC3339(raw)
				if parseErr != nil {
					s.writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "after 参数必须是合法的 RFC3339 时间")
					return false
				}
				utc := parsed.UTC()
				reqCtx.Filter.After = &utc
			}
		case "sender":
			reqCtx.Filter.SenderFilter = mergeQueryFilter(reqCtx.Filter.SenderFilter, raw)
		case "keyword", "subject":
			reqCtx.Filter.SubjectFilter = mergeQueryFilter(reqCtx.Filter.SubjectFilter, raw)
		case "tag":
			reqCtx.Filter.Tag = strings.TrimPrefix(raw, "+")
		case "to":
			if raw != "" {
				if extracted := extractSubaddressTag(rootAddress, raw); extracted != "" {
					reqCtx.Filter.Tag = extracted
				}
			}
		case "latest":
			if raw != "" {
				parsed, parseErr := strconv.ParseBool(raw)
				if parseErr != nil {
					s.writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "latest 参数必须是布尔值")
					return false
				}
				reqCtx.ReturnLatest = parsed
			}
		case "wait_seconds":
			if raw != "" {
				if strings.Trim(raw, "0123456789") != "" {
					s.writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "wait_seconds 必须是 0 到 15 的整数")
					return false
				}
				sec, parseErr := strconv.Atoi(raw)
				if parseErr != nil || sec < 0 || sec > 15 {
					s.writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "wait_seconds 必须是 0 到 15 的整数")
					return false
				}
				reqCtx.WaitDuration = time.Duration(sec) * time.Second
			}
		default:
			s.writeAPIError(c, http.StatusBadRequest, "INVALID_REQUEST", "不支持的查询参数: "+key)
			return false
		}
	}
	return true
}

func parseFlexibleRFC3339(raw string) (time.Time, error) {
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	return time.Parse(time.RFC3339, raw)
}

func mergeQueryFilter(existing, extra string) string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return existing
	}
	if strings.TrimSpace(existing) == "" {
		return extra
	}
	return existing + "," + extra
}

func extractSubaddressTag(rootAddress, candidate string) string {
	candidate = domain.NormalizeEmail(candidate)
	root := domain.NormalizeEmail(rootAddress)
	at := strings.LastIndexByte(candidate, '@')
	rootAt := strings.LastIndexByte(root, '@')
	if at <= 0 || rootAt <= 0 || candidate[at+1:] != root[rootAt+1:] {
		return ""
	}
	local := candidate[:at]
	plus := strings.IndexByte(local, '+')
	if plus <= 0 || local[:plus] != root[:rootAt] {
		return ""
	}
	return strings.TrimSpace(local[plus+1:])
}

func (s *Server) issueIMAPAccessToken(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	if !s.externalAPILimiter.Allow(c.ClientIP()) {
		writeOAuthError(c, http.StatusTooManyRequests, "temporarily_unavailable", "请求过于频繁")
		return
	}
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" || c.Request.URL.RawQuery != "" {
		writeOAuthError(c, http.StatusBadRequest, "invalid_request", "请求必须使用表单编码")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	if err := c.Request.ParseForm(); err != nil {
		writeOAuthError(c, http.StatusBadRequest, "invalid_request", "请求表单无效")
		return
	}
	clientIDs := c.Request.PostForm["client_id"]
	refreshTokens := c.Request.PostForm["refresh_token"]
	grantTypes := c.Request.PostForm["grant_type"]
	if len(clientIDs) != 1 || len(refreshTokens) != 1 || len(grantTypes) != 1 ||
		grantTypes[0] != "refresh_token" || len(c.Request.PostForm) != 3 {
		writeOAuthError(c, http.StatusBadRequest, "invalid_request", "client_id、refresh_token 和 grant_type 必须各出现一次")
		return
	}
	clientID := strings.TrimSpace(clientIDs[0])
	refreshToken := refreshTokens[0]
	if clientID == "" || refreshToken == "" || strings.TrimSpace(refreshToken) != refreshToken ||
		strings.ContainsAny(clientID+refreshToken, " \t\r\n") {
		writeOAuthError(c, http.StatusUnauthorized, "invalid_client", "客户端凭证无效")
		return
	}
	binding, err := s.store.GetMailboxBindingByOAuthClientID(c.Request.Context(), clientID)
	if errors.Is(err, store.ErrNotFound) || err == nil &&
		(binding.Alias.CredentialMode != domain.AliasCredentialModeV2 ||
			!binding.Alias.Enabled || !binding.Account.Enabled ||
			!secure.HashEqual(binding.Alias.RefreshTokenHash, secure.HashToken(refreshToken))) {
		writeOAuthError(c, http.StatusUnauthorized, "invalid_client", "客户端凭证无效")
		return
	}
	if err != nil {
		s.logger.Error("查询令牌凭证失败", "error", err, "request_id", requestID(c))
		writeOAuthError(c, http.StatusServiceUnavailable, "temporarily_unavailable", "令牌服务暂不可用")
		return
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	const lifetime = time.Hour
	accessToken, err := s.cipher.IssueAliasAccessToken(
		binding.Alias.ID,
		binding.Alias.CredentialVersion,
		binding.Alias.RefreshTokenHash,
		now.Add(lifetime),
	)
	if err != nil {
		s.logger.Error("签发 IMAPS 访问令牌失败", "error", err, "request_id", requestID(c))
		writeOAuthError(c, http.StatusServiceUnavailable, "temporarily_unavailable", "令牌服务暂不可用")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"access_token": accessToken,
		"token_type":   "Bearer",
		"expires_in":   int(lifetime.Seconds()),
	})
}

func writeOAuthError(c *gin.Context, status int, code, description string) {
	c.JSON(status, gin.H{"error": code, "error_description": description})
}
