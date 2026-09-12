package httpserver

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"icloud-api/internal/domain"
	"icloud-api/internal/store"
)

func (s *Server) poolLock() gin.HandlerFunc {
	return func(c *gin.Context) {
		s.poolMu.Lock()
		defer s.poolMu.Unlock()
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

func (s *Server) poolAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.externalAPILimiter.Allow(c.ClientIP()) {
			s.writeAPIError(c, 429, "RATE_LIMITED", "请求过于频繁")
			c.Abort()
			return
		}
		token, ok := strictBearerToken(c.Request)
		if !ok || !strings.HasPrefix(token, "pool_") {
			s.writeAPIError(c, 401, "INVALID_POOL_KEY", "项目 Key 无效")
			c.Abort()
			return
		}
		client, err := s.store.AuthenticatePoolClient(c.Request.Context(), token)
		if errors.Is(err, store.ErrNotFound) {
			s.writeAPIError(c, 401, "INVALID_POOL_KEY", "项目 Key 无效")
			c.Abort()
			return
		}
		if err != nil {
			s.poolError(c, err)
			c.Abort()
			return
		}
		c.Set("pool_client", client)
		c.Next()
	}
}

func poolClient(c *gin.Context) store.PoolClient { return c.MustGet("pool_client").(store.PoolClient) }

func (s *Server) registerPoolRoutes(api *gin.RouterGroup) {
	api.Use(s.poolLock(), s.poolAuth())
	api.POST("/claim", s.poolClaim)
	api.GET("/leases", s.poolListLeases)
	api.GET("/leases/:leaseID", s.poolGetLease)
	api.GET("/leases/:leaseID/code", s.poolLeaseCode)
	api.POST("/leases/:leaseID/commit", s.poolLeaseAction("commit", false))
	api.POST("/leases/:leaseID/release", s.poolLeaseAction("release", false))
	api.POST("/leases/:leaseID/renew", s.poolLeaseAction("renew", false))
}

func (s *Server) registerAdminPoolRoutes(api *gin.RouterGroup) {
	api.Use(s.poolLock())
	api.GET("/accounts", s.adminPoolAccounts)
	api.PUT("/accounts/:id", s.adminSetPoolAccount)
	api.GET("/members", s.adminPoolMembers)
	api.POST("/members", s.adminEnrollPoolMembers)
	api.PATCH("/members/:id", s.adminSetPoolMember)
	api.GET("/leases", s.adminPoolLeases)
	api.POST("/leases/:leaseID/release", s.poolLeaseAction("release", true))
	api.POST("/leases/:leaseID/commit", s.poolLeaseAction("commit", true))
	api.GET("/clients", s.adminPoolClients)
	api.POST("/clients", s.adminCreatePoolClient)
	api.PATCH("/clients/:clientID", s.adminUpdatePoolClient)
}

func (s *Server) poolError(c *gin.Context, err error) {
	status, code, message := 503, "POOL_UNAVAILABLE", "邮箱池暂不可用"
	switch {
	case errors.Is(err, store.ErrPoolInput):
		status, code, message = 400, "INVALID_POOL_REQUEST", "请检查数量、项目名称、领取时间和请求编号"
	case errors.Is(err, store.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "记录不存在"
	case errors.Is(err, store.ErrPoolEmpty):
		status, code, message = 409, "POOL_EMPTY", "空闲邮箱数量不足"
	case errors.Is(err, store.ErrPoolRequestConflict):
		status, code, message = 409, "IDEMPOTENCY_CONFLICT", "请求编号已用于其他参数，请使用原参数重试"
	case errors.Is(err, store.ErrPoolConflict):
		status, code, message = 409, "POOL_CONFLICT", "邮箱状态已变化，请刷新后操作"
	case errors.Is(err, store.ErrPoolClosed):
		status, code, message = 409, "LEASE_CLOSED", "领取已结束或到期"
	default:
		s.logger.Error("邮箱池操作失败", "error", err, "request_id", requestID(c))
	}
	s.writeAPIError(c, status, code, message)
}

func poolPagination(c *gin.Context) (int, int) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	if offset > 1000000 {
		offset = 1000000
	}
	return limit, offset
}

func (s *Server) poolClaim(c *gin.Context) {
	input := store.PoolClaim{Count: 1, TTLSeconds: 1800}
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	if err := s.store.RefreshPool(c.Request.Context()); err != nil {
		s.poolError(c, err)
		return
	}
	leases, err := s.store.ClaimPool(c.Request.Context(), poolClient(c).ID, input)
	if err != nil {
		s.poolError(c, err)
		return
	}
	result := make([]gin.H, 0, len(leases))
	for _, lease := range leases {
		v, err := s.poolLeasePayload(c, lease)
		if err != nil {
			s.poolError(c, err)
			return
		}
		result = append(result, v)
	}
	s.audit(c, nil, poolClient(c).Project, "pool_claim", "pool_request", input.RequestID, "success", "")
	c.JSON(200, gin.H{"data": gin.H{"leases": result}})
}

func (s *Server) poolLeasePayload(c *gin.Context, lease store.PoolLease) (gin.H, error) {
	result := gin.H{"lease": lease}
	if lease.AliasID == 0 || lease.State != "used" && (lease.State != "leased" || !s.now().Before(lease.ExpiresAt)) {
		return result, nil
	}
	alias, err := s.store.GetAlias(c.Request.Context(), lease.AliasID)
	if errors.Is(err, store.ErrNotFound) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	account, err := s.store.GetAccount(c.Request.Context(), alias.AccountID)
	if err != nil {
		return nil, err
	}
	if !alias.Enabled || !account.Enabled || alias.CredentialMode != domain.AliasCredentialModeV2 {
		return result, nil
	}
	dto, err := s.adminAPIAliasFromDomain(alias)
	if err != nil {
		return nil, err
	}
	result["mailbox"] = gin.H{"alias_id": alias.ID, "email": alias.Address, "api_key": dto.APIKey,
		"imap_password": dto.IMAPPassword, "client_id": dto.ClientID, "refresh_token": dto.RefreshToken,
		"credential_version": dto.CredentialVersion, "otp_path": dto.OTPURLPath,
		"code_path": "/api/v1/pool/leases/" + lease.ID + "/code", "oauth_token_path": "/oauth2/v2.0/token",
		"imap_host": s.cfg.PublicIMAPServerName, "imap_port": 1993, "imap_tls": true}
	return result, nil
}

func (s *Server) poolListLeases(c *gin.Context) {
	limit, offset := poolPagination(c)
	page, err := s.store.ListPoolLeases(c.Request.Context(), poolClient(c).ID, c.Query("state"), limit, offset)
	if err != nil {
		s.poolError(c, err)
		return
	}
	c.JSON(200, gin.H{"data": page})
}

func (s *Server) poolGetLease(c *gin.Context) {
	v, err := s.store.GetPoolLease(c.Request.Context(), c.Param("leaseID"), poolClient(c).ID)
	if err != nil {
		s.poolError(c, err)
		return
	}
	result, err := s.poolLeasePayload(c, v)
	if err != nil {
		s.poolError(c, err)
		return
	}
	c.JSON(200, gin.H{"data": result})
}

func (s *Server) poolLeaseAction(action string, admin bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		clientID := ""
		actor := "admin"
		if !admin {
			clientID = poolClient(c).ID
			actor = poolClient(c).Project
		}
		input := struct {
			TTLSeconds int `json:"ttl_seconds"`
		}{TTLSeconds: 1800}
		if action == "renew" && !decodeAdminAPIJSON(c, &input) {
			return
		}
		v, err := s.store.ActPoolLease(c.Request.Context(), c.Param("leaseID"), clientID, action, input.TTLSeconds)
		if err != nil {
			s.poolError(c, err)
			return
		}
		s.audit(c, nil, actor, "pool_"+action, "pool_lease", v.ID, "success", "")
		c.JSON(200, gin.H{"data": v})
	}
}

func (s *Server) poolLeaseCode(c *gin.Context) {
	v, err := s.store.GetPoolLease(c.Request.Context(), c.Param("leaseID"), poolClient(c).ID)
	if err != nil {
		s.poolError(c, err)
		return
	}
	if v.AliasID == 0 || v.State != "used" && (v.State != "leased" || !s.now().Before(v.ExpiresAt)) {
		s.poolError(c, store.ErrPoolClosed)
		return
	}
	alias, err := s.store.GetAlias(c.Request.Context(), v.AliasID)
	if err != nil {
		s.poolError(c, err)
		return
	}
	account, err := s.store.GetAccount(c.Request.Context(), alias.AccountID)
	if err != nil {
		s.poolError(c, err)
		return
	}
	if !alias.Enabled || !account.Enabled {
		s.poolError(c, store.ErrPoolClosed)
		return
	}
	after := v.CreatedAt
	if raw := c.Query("after"); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			s.poolError(c, store.ErrPoolInput)
			return
		}
		if parsed.After(after) {
			after = parsed
		}
	}
	s.requestMailboxSync(alias.AccountID, s.now())
	records, err := s.store.ListAliasOTPs(c.Request.Context(), alias.ID, 100)
	if err != nil {
		s.poolError(c, err)
		return
	}
	for _, record := range records {
		if record.Time.After(after) {
			c.JSON(200, gin.H{"data": gin.H{"success": true, "otp": record.OTP, "time": record.Time.UTC().Format(time.RFC3339Nano), "email": alias.Address}})
			return
		}
	}
	c.JSON(200, gin.H{"data": gin.H{"success": false, "code": "no_code", "retryable": true, "email": alias.Address}})
}

func (s *Server) adminPoolAccounts(c *gin.Context) {
	rows, err := s.store.ListPoolAccounts(c.Request.Context())
	if err != nil {
		s.poolError(c, err)
		return
	}
	result := make([]gin.H, 0, len(rows))
	for _, v := range rows {
		row := gin.H{"account_id": v.AccountID, "email": v.Email, "auto_enroll": v.AutoEnroll, "target": v.Target, "available": v.Available}
		if s.autoCreate != nil {
			schedule, err := s.autoCreate.GetSchedule(c.Request.Context(), v.AccountID)
			if err != nil {
				s.poolError(c, err)
				return
			}
			row["auto_create"] = schedule.Enabled
			row["next_run_at"] = schedule.NextRunAt
			row["last_error"] = schedule.LastError
			row["creation_status"] = adminAPIAutoCreationFromSchedule(schedule, "").Status
		}
		result = append(result, row)
	}
	c.JSON(200, gin.H{"data": result})
}

func (s *Server) adminSetPoolAccount(c *gin.Context) {
	id, ok := adminAPIParseID(c)
	if !ok {
		return
	}
	var input struct {
		AutoEnroll bool `json:"auto_enroll"`
		Target     int  `json:"target"`
	}
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	if err := s.store.SetPoolAccount(c.Request.Context(), id, input.AutoEnroll, input.Target); err != nil {
		s.poolError(c, err)
		return
	}
	s.audit(c, nil, "admin", "pool_settings", "account", strconv.FormatInt(id, 10), "success", "")
	c.JSON(200, gin.H{"data": gin.H{"saved": true}})
}

func (s *Server) adminPoolMembers(c *gin.Context) {
	limit, offset := poolPagination(c)
	page, err := s.store.ListPoolMembers(c.Request.Context(), c.Query("state"), c.Query("q"), limit, offset)
	if err != nil {
		s.poolError(c, err)
		return
	}
	c.JSON(200, gin.H{"data": page})
}
func (s *Server) adminPoolLeases(c *gin.Context) {
	limit, offset := poolPagination(c)
	page, err := s.store.ListPoolLeases(c.Request.Context(), c.Query("client_id"), c.Query("state"), limit, offset)
	if err != nil {
		s.poolError(c, err)
		return
	}
	c.JSON(200, gin.H{"data": page})
}
func (s *Server) adminEnrollPoolMembers(c *gin.Context) {
	var input struct {
		AliasIDs []int64 `json:"alias_ids"`
	}
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	if err := s.store.EnrollPoolAliases(c.Request.Context(), input.AliasIDs); err != nil {
		s.poolError(c, err)
		return
	}
	s.audit(c, nil, "admin", "pool_enroll", "pool", "", "success", strconv.Itoa(len(input.AliasIDs)))
	c.JSON(200, gin.H{"data": gin.H{"saved": true}})
}
func (s *Server) adminSetPoolMember(c *gin.Context) {
	id, ok := adminAPIParseID(c)
	if !ok {
		return
	}
	var input struct {
		State string `json:"state"`
	}
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	if err := s.store.SetPoolMember(c.Request.Context(), id, input.State); err != nil {
		s.poolError(c, err)
		return
	}
	c.JSON(200, gin.H{"data": gin.H{"saved": true}})
}
func (s *Server) adminPoolClients(c *gin.Context) {
	rows, err := s.store.ListPoolClients(c.Request.Context())
	if err != nil {
		s.poolError(c, err)
		return
	}
	c.JSON(200, gin.H{"data": rows})
}
func (s *Server) adminCreatePoolClient(c *gin.Context) {
	var input struct {
		Project string `json:"project"`
	}
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	client, token, err := s.store.CreatePoolClient(c.Request.Context(), input.Project)
	if err != nil {
		s.poolError(c, err)
		return
	}
	s.audit(c, nil, "admin", "pool_client_create", "pool_client", client.ID, "success", "")
	c.JSON(201, gin.H{"data": gin.H{"client": client, "api_key": token}})
}
func (s *Server) adminUpdatePoolClient(c *gin.Context) {
	var input struct {
		Enabled bool `json:"enabled"`
		Rotate  bool `json:"rotate"`
	}
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	token, err := s.store.UpdatePoolClient(c.Request.Context(), c.Param("clientID"), input.Enabled, input.Rotate)
	if err != nil {
		s.poolError(c, err)
		return
	}
	s.audit(c, nil, "admin", "pool_client_update", "pool_client", c.Param("clientID"), "success", "")
	c.JSON(200, gin.H{"data": gin.H{"api_key": token}})
}

func (s *Server) RunPoolMaintenance(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		func() {
			s.credentialRotationMu.RLock()
			defer s.credentialRotationMu.RUnlock()
			s.poolMu.Lock()
			defer s.poolMu.Unlock()
			round, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := s.store.RefreshPool(round); err != nil && ctx.Err() == nil {
				s.logger.Error("邮箱池回收失败", "error", err)
			}
		}()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
