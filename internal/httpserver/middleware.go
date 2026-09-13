package httpserver

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"icloud-api/internal/domain"
	"icloud-api/internal/secure"
	"icloud-api/internal/store"
)

const (
	requestIDKey  = "request_id"
	sessionKey    = "admin_session"
	bindingKey    = "mailbox_binding"
	sessionCookie = "icloud_admin_session"
)

type limiterEntry struct {
	count int
	reset time.Time
}

type windowLimiter struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	maxItems    int
	nextCleanup time.Time
	items       map[string]limiterEntry
}

type requestSample struct {
	at         time.Time
	suppressed int
}
type requestSampler struct {
	mu          sync.Mutex
	items       map[string]requestSample
	max         int
	nextCleanup time.Time
}

func newRequestSampler() *requestSampler {
	return &requestSampler{items: make(map[string]requestSample), max: 2048}
}
func (s *requestSampler) allow(key string, now time.Time) (bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !now.Before(s.nextCleanup) {
		for k, v := range s.items {
			if now.Sub(v.at) >= 5*time.Minute && k != key {
				delete(s.items, k)
			}
		}
		s.nextCleanup = now.Add(time.Minute)
	}
	v, ok := s.items[key]
	if ok && now.Sub(v.at) < time.Minute {
		v.suppressed++
		s.items[key] = v
		return false, 0
	}
	if !ok && len(s.items) >= s.max {
		return false, 0
	}
	n := v.suppressed
	s.items[key] = requestSample{at: now}
	return true, n
}

var routeAccountID = regexp.MustCompile(`/accounts/([0-9]+)`)

var httpOperations = map[string]string{
	"GET /auth/csrf": "准备后台登录", "POST /auth/login": "登录后台", "GET /auth/session": "确认后台登录状态", "POST /auth/logout": "退出后台", "PUT /auth/password": "修改管理员密码",
	"POST /accounts/:id/sync":          "请求同步主号邮件",
	"PUT /accounts/:id/mail-transport": "切换邮件接收通道",
	"POST /accounts/:id/apple-auth":    "登录 iCloud Web 通道", "POST /accounts/:id/apple-auth/verify": "验证 iCloud Web 登录", "DELETE /accounts/:id/apple-auth": "退出 iCloud Web 通道",
	"GET /accounts/:id/apple-account-auth": "读取 Apple Account 登录状态", "POST /accounts/:id/apple-account-auth": "登录 Apple Account 通道", "POST /accounts/:id/apple-account-auth/verify": "验证 Apple Account 登录", "DELETE /accounts/:id/apple-account-auth": "退出 Apple Account 通道",
	"PUT /accounts/:id/aliases/auto-create": "设置自动创建计划", "POST /accounts/:id/aliases/create-now": "手动创建隐私邮箱", "POST /accounts/:id/aliases/creation-job/stop": "停止批量创建",
	"GET /accounts/:id/aliases/auto-create/keys": "读取新邮箱凭据", "DELETE /accounts/:id/aliases/auto-create/keys": "确认领取新邮箱凭据",
	"POST /accounts/:id/aliases": "登记已有邮箱", "POST /accounts/:id/aliases/random": "生成自定义邮箱", "POST /accounts/:id/aliases/batch": "批量生成自定义邮箱", "POST /accounts/:id/aliases/generate": "生成自定义邮箱", "POST /accounts/:id/aliases/sync": "同步 Apple 隐私邮箱目录",
	"GET /groups": "读取邮箱分组", "POST /groups": "创建邮箱分组", "PATCH /groups/:id": "重命名邮箱分组", "DELETE /groups/:id": "删除邮箱分组",
	"PATCH /aliases/group": "批量移动邮箱分组", "PATCH /aliases/:id/group": "移动邮箱分组", "DELETE /aliases/batch": "提交 Apple 批量永久删除", "GET /aliases/batch/jobs/latest": "恢复最近批量删除任务", "GET /aliases/batch/jobs/:jobID": "读取批量删除进度",
	"POST /aliases/rotate-all-credentials": "轮换全部邮箱凭据", "POST /aliases/:id/rotate-key": "轮换单邮箱 API Key", "POST /aliases/:id/rotate-credentials": "轮换单邮箱完整凭据", "GET /audit": "读取管理操作记录",
	"GET /pool/leases/:leaseID/code": "查询领取邮箱验证码", "POST /pool/leases/:leaseID/commit": "确认使用领取邮箱", "POST /pool/leases/:leaseID/release": "释放领取邮箱并轮换凭据", "POST /pool/leases/:leaseID/renew": "续期领取邮箱",
	"POST /oauth2/v2.0/token": "换取 IMAP OAuth 访问凭据", "GET /docs": "查看 API 文档",
	"GET /accounts": "读取主号列表", "POST /accounts": "创建主号", "GET /accounts/:id": "读取主号详情", "PUT /accounts/:id": "更新主号", "DELETE /accounts/:id": "删除主号",
	"GET /aliases": "读取隐私邮箱列表", "POST /aliases": "创建隐私邮箱", "GET /aliases/:id": "读取隐私邮箱", "PATCH /aliases/:id": "更新隐私邮箱", "DELETE /aliases/:id": "删除隐私邮箱",
	"GET /accounts/:id/aliases/creation-job": "读取批量创建进度", "POST /accounts/:id/aliases/creation-job": "提交批量创建",
	"GET /otp": "查询验证码", "GET /mail/latest": "获取最新邮件", "GET /mail/recent": "获取最近邮件", "POST /pool/claim": "领取邮箱",
	"GET /logs": "读取日志", "GET /health": "健康检查", "GET /healthz": "健康检查",
	"GET /pool/accounts": "读取邮箱池主号", "PUT /pool/accounts/:id": "更新邮箱池主号", "GET /pool/members": "读取邮箱池成员", "POST /pool/members": "添加邮箱池成员", "PATCH /pool/members/:id": "更新邮箱池成员", "GET /pool/leases": "读取邮箱池租约", "GET /pool/leases/:leaseID": "读取邮箱池租约详情", "GET /pool/clients": "读取邮箱池客户端", "POST /pool/clients": "创建邮箱池客户端", "PATCH /pool/clients/:clientID": "更新邮箱池客户端",
}

func httpOperation(path, method string) string {
	p := strings.TrimSuffix(path, "/")
	for _, prefix := range []string{"/api/v1", "/admin/api/v1"} {
		if i := strings.Index(p, prefix); i >= 0 {
			p = p[i+len(prefix):]
			break
		}
	}
	if op, ok := httpOperations[method+" "+p]; ok {
		return op
	}
	return "其他请求"
}

func newWindowLimiter(limit int, window time.Duration) *windowLimiter {
	return &windowLimiter{limit: limit, window: window, maxItems: 8192, items: make(map[string]limiterEntry)}
}

func (l *windowLimiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.nextCleanup.IsZero() || !now.Before(l.nextCleanup) {
		for itemKey, item := range l.items {
			if !item.reset.After(now) {
				delete(l.items, itemKey)
			}
		}
		cleanupInterval := min(l.window, time.Minute)
		if cleanupInterval <= 0 {
			cleanupInterval = time.Minute
		}
		l.nextCleanup = now.Add(cleanupInterval)
	}
	entry := l.items[key]
	if _, exists := l.items[key]; !exists && len(l.items) >= l.maxItems {
		return false
	}
	if entry.reset.Before(now) {
		entry = limiterEntry{reset: now.Add(l.window)}
	}
	entry.count++
	l.items[key] = entry
	return entry.count <= l.limit
}

func (s *Server) requestContext() gin.HandlerFunc {
	sampler := newRequestSampler()
	return func(c *gin.Context) {
		requestID, err := secure.RandomToken(12)
		if err != nil {
			requestID = "request-id-unavailable"
		}
		c.Set(requestIDKey, requestID)
		c.Request = c.Request.WithContext(domain.WithMailboxRequestID(c.Request.Context(), requestID))
		c.Header("X-Request-ID", requestID)
		started := time.Now()
		c.Next()
		status := c.Writer.Status()
		duration := time.Since(started)
		operation := httpOperation(c.FullPath(), c.Request.Method)
		if c.FullPath() == "" {
			operation = "访问未匹配路由"
		} else if c.FullPath() == s.cfg.AdminPath+"/assets/*filepath" {
			operation = "读取后台静态资源"
		} else if c.FullPath() == s.cfg.AdminPath || c.FullPath() == s.cfg.AdminPath+"/" || c.FullPath() == "/" {
			operation = "打开后台页面"
		}
		if status >= 200 && status < 400 && duration < time.Second && (operation == "健康检查" || operation == "读取日志" || strings.HasPrefix(c.Request.URL.Path, s.cfg.AdminPath+"/assets/")) {
			return
		}
		attrs := []any{"method", c.Request.Method, "path", s.redactedRequestPath(c.Request.URL.Path), "status", status, "duration_ms", duration.Milliseconds(), "request_id", requestID, "operation", operation}
		// GET collection/status endpoints are sampled per route and main account.
		key := c.FullPath() + " " + c.Request.Method
		if m := routeAccountID.FindStringSubmatch(c.Request.URL.Path); len(m) > 1 {
			key += " " + m[1]
			if id, err := strconv.ParseInt(m[1], 10, 64); err == nil && id > 0 {
				attrs = append(attrs, "account_id", id)
			}
		} else if binding, ok := c.Get(bindingKey); ok {
			if mailbox, ok := binding.(domain.MailboxBinding); ok {
				attrs = append(attrs, "account_id", mailbox.Account.ID, "alias_id", mailbox.Alias.ID)
				key += " " + strconv.FormatInt(mailbox.Account.ID, 10)
			}
		}
		if c.Request.Method == http.MethodGet && status < http.StatusBadRequest && duration < time.Second {
			if ok, suppressed := sampler.allow(key, time.Now()); !ok {
				return
			} else if suppressed > 0 {
				attrs = append(attrs, "suppressed_count", suppressed)
			}
		}
		if status >= 500 {
			s.logger.Error(operation, attrs...)
		} else if status >= 400 {
			s.logger.Warn(operation, attrs...)
		} else {
			s.logger.Info(operation, attrs...)
		}
	}
}

func (s *Server) securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		if pathWithin(c.Request.URL.Path, s.cfg.AdminPath) ||
			pathWithin(c.Request.URL.Path, legacyAdminAPIBasePath) {
			c.Header("Cache-Control", "no-store, private")
			c.Header("Pragma", "no-cache")
		}
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'self'")
		c.Next()
	}
}

func sameOrigin(r *http.Request) bool {
	return originFailureReason(r) == ""
}

func originFailureReason(r *http.Request) string {
	if fetchSite := strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")); strings.EqualFold(fetchSite, "cross-site") {
		return loginCSRFFetchSiteCrossSite
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return ""
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" ||
		(!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) ||
		parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return loginCSRFOriginInvalid
	}
	if !strings.EqualFold(parsed.Host, r.Host) {
		return loginCSRFOriginHostMismatch
	}
	return ""
}

func validAPIKey(token string) bool {
	const (
		prefix       = "icm_"
		secretLength = 43
	)
	if len(token) != len(prefix)+secretLength || !strings.HasPrefix(token, prefix) {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token[len(prefix):])
	return err == nil && len(decoded) == 32
}

// apiKeyAuth authenticates the original Bearer API Key contract. It is kept
// separate from the v2 OTP parser so old clients retain their exact header
// shape and error behavior.
func (s *Server) apiKeyAuth() gin.HandlerFunc {
	return s.apiKeyAuthWithToken(func(c *gin.Context) (string, bool) {
		scheme, token, ok := strings.Cut(c.GetHeader("Authorization"), " ")
		return token, ok && strings.EqualFold(scheme, "Bearer")
	}, false)
}

// apiKeyQueryAuth accepts the legacy direct-link query parameter.
func (s *Server) apiKeyQueryAuth() gin.HandlerFunc {
	return s.apiKeyAuthWithToken(func(c *gin.Context) (string, bool) {
		values, err := url.ParseQuery(c.Request.URL.RawQuery)
		if err != nil {
			return "", false
		}
		keys, ok := values["api_key"]
		if !ok || len(keys) != 1 {
			return "", false
		}
		return keys[0], true
	}, true)
}

func (s *Server) apiKeyAuthWithToken(
	tokenFromRequest func(*gin.Context) (string, bool),
	allowDirectLinkToken bool,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		token, ok := tokenFromRequest(c)
		if !ok || !validAPIKey(token) {
			s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "API Key 无效")
			c.Abort()
			return
		}
		binding, err := s.mailboxBindingForToken(c, token, allowDirectLinkToken)
		if errors.Is(err, store.ErrNotFound) || (err == nil && (!binding.Alias.Enabled || !binding.Account.Enabled)) {
			s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "API Key 无效")
			c.Abort()
			return
		}
		if err != nil {
			s.logger.Error("查询 API Key 绑定失败", "error", err, "request_id", requestID(c))
			s.writeAPIError(c, http.StatusServiceUnavailable, "DATABASE_UNAVAILABLE", "数据库暂不可用")
			c.Abort()
			return
		}
		c.Set(bindingKey, binding)
		c.Next()
	}
}

func (s *Server) mailboxBindingForToken(c *gin.Context, token string, allowDirectLinkToken bool) (domain.MailboxBinding, error) {
	if allowDirectLinkToken && s.cipher != nil {
		if aliasID, candidate := secure.RecentMailTokenAliasID(token); candidate {
			alias, err := s.store.GetAlias(c.Request.Context(), aliasID)
			switch {
			case err == nil && alias.CredentialMode == domain.AliasCredentialModeV2 &&
				s.cipher.VerifyRecentMailToken(token, aliasID, alias.APIKeyHash):
				return s.store.GetMailboxBindingByAPIKeyHash(c.Request.Context(), alias.APIKeyHash)
			case err != nil && !errors.Is(err, store.ErrNotFound):
				return domain.MailboxBinding{}, err
			}
		}
		if aliasID, candidate := secure.DirectLinkTokenAliasID(token); candidate {
			alias, err := s.store.GetAlias(c.Request.Context(), aliasID)
			switch {
			case err == nil && alias.CredentialMode == domain.AliasCredentialModeLegacy &&
				s.cipher.VerifyDirectLinkToken(token, aliasID, alias.APIKeyHash):
				return s.store.GetMailboxBindingByAPIKeyHash(c.Request.Context(), alias.APIKeyHash)
			case err != nil && !errors.Is(err, store.ErrNotFound):
				return domain.MailboxBinding{}, err
			}
		}
	}
	return s.store.GetMailboxBindingByAPIKeyHash(c.Request.Context(), secure.HashToken(token))
}

func mustSession(c *gin.Context) domain.Session {
	value, _ := c.Get(sessionKey)
	session, _ := value.(domain.Session)
	return session
}

func mustBinding(c *gin.Context) domain.MailboxBinding {
	value, _ := c.Get(bindingKey)
	binding, _ := value.(domain.MailboxBinding)
	return binding
}

func requestID(c *gin.Context) string {
	value, _ := c.Get(requestIDKey)
	requestID, _ := value.(string)
	return requestID
}

func (s *Server) writeAPIError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message, "request_id": requestID(c)}})
}
