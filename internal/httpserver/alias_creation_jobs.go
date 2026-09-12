package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/hmesync"
	"icloud-api/internal/store"
)

type aliasCreationJobRuntime struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   map[int64]context.CancelFunc
	stopping bool
	wg       sync.WaitGroup
	interval time.Duration
}
type channelAliasCreator interface {
	CreateAliasWithChannel(context.Context, int64, string) (domain.Alias, error)
}

func (s *Server) StartAliasCreationJobs(ctx context.Context) error {
	r := &s.aliasCreationJobs
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx != nil {
		return errors.New("alias creation jobs already started")
	}
	if err := s.store.EnsureAliasCreationJobs(ctx); err != nil {
		return err
	}
	if err := s.store.InterruptAliasCreationJobs(ctx); err != nil {
		return err
	}
	r.ctx = ctx
	r.cancel = make(map[int64]context.CancelFunc)
	r.interval = 3 * time.Second
	return nil
}

func (s *Server) RunAliasCreationJobs() {
	r := &s.aliasCreationJobs
	r.mu.Lock()
	ctx := r.ctx
	r.mu.Unlock()
	if ctx == nil {
		return
	}
	ticker := time.NewTicker(4 * time.Minute)
	defer ticker.Stop()
keepalive:
	for {
		select {
		case <-ctx.Done():
			break keepalive
		case <-ticker.C:
			s.keepAliveAppleAccounts(ctx)
		}
	}
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
	r.wg.Wait()
}

func (s *Server) keepAliveAppleAccounts(ctx context.Context) {
	service, ok := s.hmeSync.(interface {
		KeepAliveAccountSession(context.Context, int64) error
	})
	if !ok {
		return
	}
	accounts, err := s.store.ListEnabledAccounts(ctx)
	if err != nil {
		return
	}
	for _, account := range accounts {
		if ctx.Err() != nil {
			return
		}
		if domain.NormalizeMailboxType(account.MailboxType) != domain.MailboxTypeICloud {
			continue
		}
		s.credentialRotationMu.RLock()
		refreshCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
		err := service.KeepAliveAccountSession(refreshCtx, account.ID)
		cancel()
		s.credentialRotationMu.RUnlock()
		if err != nil && ctx.Err() == nil {
			s.logger.Warn("Apple Account 会话保活未完成", "account_id", account.ID)
		}
	}
}

func (s *Server) adminAPIStartAliasCreationJob(c *gin.Context) {
	id, ok := adminAPIParseID(c)
	if !ok {
		return
	}
	var in struct {
		Count   int    `json:"count"`
		Channel string `json:"channel"`
	}
	if !decodeAdminAPIJSON(c, &in) {
		return
	}
	if in.Count < 1 || in.Count > 100 || in.Channel != "auto" && in.Channel != "apple_account" && in.Channel != "icloud_web" {
		writeAdminAPIError(c, 400, "VALIDATION_FAILED", "数量应为 1-100，且创建通道有效")
		return
	}
	account, err := s.store.GetAccount(c.Request.Context(), id)
	if err != nil {
		s.writeAdminAPIStoreReadError(c, err)
		return
	}
	if !account.Enabled || domain.NormalizeMailboxType(account.MailboxType) != domain.MailboxTypeICloud {
		writeAdminAPIError(c, 409, "ACCOUNT_DISABLED", "请选择已启用的 iCloud 主号")
		return
	}
	creator, ok := s.hmeSync.(channelAliasCreator)
	if !ok {
		writeAdminAPIError(c, 503, "APPLE_SERVICE_UNAVAILABLE", "创建服务暂未就绪")
		return
	}
	if in.Channel == "apple_account" {
		if auth, ok := s.hmeSync.(appleAccountAuthService); ok {
			info, err := auth.GetAccountSession(c.Request.Context(), id)
			if err != nil {
				apiErr := classifyAccountAuthError(err)
				writeAdminAPIError(c, apiErr.Status, apiErr.Code, apiErr.Message)
				return
			}
			if info.Status != hmesync.StatusAuthenticated {
				writeAdminAPIError(c, 409, hmesync.CodeAccountLoginRequired, "请先登录 Apple Account 新通道")
				return
			}
		}
	}
	r := &s.aliasCreationJobs
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx == nil || r.ctx.Err() != nil || r.stopping {
		writeAdminAPIError(c, 503, "SERVICE_DRAINING", "后台任务服务暂未就绪")
		return
	}
	s.manualAliasMu.Lock()
	if s.manualAliasesRunning[id] {
		s.manualAliasMu.Unlock()
		writeAdminAPIError(c, 409, "ALIAS_CREATION_BUSY", "此主号已有创建任务")
		return
	}
	if s.manualAliasesRunning == nil {
		s.manualAliasesRunning = make(map[int64]bool)
	}
	s.manualAliasesRunning[id] = true
	s.manualAliasMu.Unlock()
	accepted := false
	defer func() {
		if !accepted {
			s.manualAliasMu.Lock()
			delete(s.manualAliasesRunning, id)
			s.manualAliasMu.Unlock()
		}
	}()
	var token [16]byte
	if _, err = rand.Read(token[:]); err != nil {
		writeAdminAPIError(c, 500, "INTERNAL_ERROR", "创建任务编号失败")
		return
	}
	now := time.Now().UTC()
	j := store.AliasCreationJob{ID: hex.EncodeToString(token[:]), AccountID: id, Target: in.Count, Channel: in.Channel, Status: "running", Entries: []store.AliasCreationJobEntry{}, CreatedAt: now, UpdatedAt: now}
	if err = s.store.CreateAliasCreationJob(c.Request.Context(), j); err != nil {
		if errors.Is(err, store.ErrAliasCreationJobConflict) {
			writeAdminAPIError(c, 409, "ALIAS_CREATION_BUSY", "此主号已有创建任务")
		} else {
			writeAdminAPIError(c, 500, "STORE_ERROR", "保存创建任务失败")
		}
		return
	}
	waitCtx, cancel := context.WithTimeout(r.ctx, 24*time.Hour)
	r.cancel[id] = cancel
	r.wg.Add(1)
	accepted = true
	go s.runAliasCreationJob(waitCtx, creator, j)
	admin := mustSession(c)
	s.audit(c, &admin.AdminID, admin.Username, "alias_creation_job_start", "account", strconv.FormatInt(id, 10), "success", j.ID)
	c.Header("Cache-Control", "no-store")
	writeAdminAPIData(c, http.StatusAccepted, gin.H{"job": j})
}

func (s *Server) adminAPIGetAliasCreationJob(c *gin.Context) {
	id, ok := adminAPIParseID(c)
	if !ok || !s.adminAPIAppleAccountExists(c, id) {
		return
	}
	c.Header("Cache-Control", "no-store")
	j, err := s.store.GetLatestAliasCreationJob(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeAdminAPIData(c, 200, gin.H{"job": nil})
		return
	}
	if err != nil {
		writeAdminAPIError(c, 500, "STORE_ERROR", "读取创建任务失败")
		return
	}
	writeAdminAPIData(c, 200, gin.H{"job": j})
}

func (s *Server) adminAPIStopAliasCreationJob(c *gin.Context) {
	id, ok := adminAPIParseID(c)
	if !ok {
		return
	}
	var in struct{}
	if !decodeAdminAPIJSON(c, &in) {
		return
	}
	r := &s.aliasCreationJobs
	r.mu.Lock()
	if cancel := r.cancel[id]; cancel != nil {
		cancel()
	} else if err := s.store.StopAliasCreationJob(c.Request.Context(), id); err != nil {
		// A checkpoint failure can leave an active row without a worker. Only
		// release that row when no in-flight owner exists under this mutex.
		r.mu.Unlock()
		writeAdminAPIError(c, 500, "STORE_ERROR", "停止任务记录失败，请稍后重试")
		return
	}
	r.mu.Unlock()
	admin := mustSession(c)
	s.audit(c, &admin.AdminID, admin.Username, "alias_creation_job_stop", "account", strconv.FormatInt(id, 10), "success", "")
	s.adminAPIGetAliasCreationJob(c)
}

func (s *Server) runAliasCreationJob(waitCtx context.Context, creator channelAliasCreator, j store.AliasCreationJob) {
	r := &s.aliasCreationJobs
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		if cancel := r.cancel[j.AccountID]; cancel != nil {
			cancel()
		}
		delete(r.cancel, j.AccountID)
		r.mu.Unlock()
		s.manualAliasMu.Lock()
		delete(s.manualAliasesRunning, j.AccountID)
		s.manualAliasMu.Unlock()
	}()
	checkpoint := func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.store.SaveAliasCreationJob(ctx, j); err != nil {
			s.logger.Error("保存创建任务进度失败，停止后续创建", "job_id", j.ID, "account_id", j.AccountID)
			return false
		}
		return true
	}
	wait := func(d time.Duration) bool {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-waitCtx.Done():
			return false
		case <-timer.C:
			return true
		}
	}
	for j.Completed < j.Target {
		if waitCtx.Err() != nil {
			break
		}
		j.Status = "running"
		j.NextRunAt = nil
		if !checkpoint() {
			return
		}
		s.credentialRotationMu.RLock()
		// A user stop only wakes the waiting loop. Let the current reservation
		// finish and persist its result before stopping subsequent work.
		createCtx, cancel := context.WithTimeout(context.WithoutCancel(waitCtx), 90*time.Second)
		a, err := creator.CreateAliasWithChannel(createCtx, j.AccountID, j.Channel)
		cancel()
		s.credentialRotationMu.RUnlock()
		if err != nil {
			apiErr := classifyManualAliasError(err)
			// Keep stage/status diagnostics, never response bodies or cookies.
			attributes := []any{"job_id", j.ID, "account_id", j.AccountID, "channel", j.Channel, "code", apiErr.Code}
			var upstream *apple.Error
			if errors.As(err, &upstream) {
				attributes = append(attributes, "apple_operation", upstream.Op, "apple_http_status", upstream.StatusCode)
			}
			s.logger.Warn("批量创建请求未完成", attributes...)
			j.LastError = apiErr.Message
			var pending interface{ PendingConfirmation() bool }
			var uncertain interface{ RemoteSideEffectPossible() bool }
			ambiguous := errors.Is(err, hmesync.ErrAliasConfirmationPending) || errors.As(err, &pending) && pending.PendingConfirmation() || errors.As(err, &uncertain) && uncertain.RemoteSideEffectPossible()
			if !ambiguous && (errors.Is(err, hmesync.ErrRateLimited) || apple.IsRateLimited(err)) {
				delay := max(61*time.Minute, apple.RetryDelay(err))
				next := time.Now().UTC().Add(delay)
				j.Status = "waiting"
				j.NextRunAt = &next
				if !checkpoint() {
					return
				}
				if !wait(delay) {
					break
				}
				continue
			}
			j.Status = "failed"
			j.NextRunAt = nil
			checkpoint()
			return
		}
		if a.ID < 1 || a.AccountID != j.AccountID || !a.Enabled || adminAPIAliasConfirmationPending(a) {
			j.Status = "failed"
			j.LastError = "创建结果待确认，请先刷新邮箱目录"
			checkpoint()
			return
		}
		for _, entry := range j.Entries {
			if entry.AliasID == a.ID {
				j.Status = "failed"
				j.LastError = "目录返回了已记录的地址，请先刷新确认"
				checkpoint()
				return
			}
		}
		j.Completed++
		j.LastError = ""
		j.Entries = append(j.Entries, store.AliasCreationJobEntry{AliasID: a.ID, Address: a.Address, Channel: j.Channel, CreatedAt: time.Now().UTC()})
		if j.Completed >= j.Target {
			j.Status = "completed"
			checkpoint()
			return
		}
		if !checkpoint() {
			return
		}
		if !wait(r.interval) {
			break
		}
	}
	j.Status = "stopped"
	j.NextRunAt = nil
	if r.ctx.Err() != nil {
		j.Status = "interrupted"
		j.LastError = "服务已重启或停止，已保留完成地址"
	} else if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
		j.Status = "interrupted"
		j.LastError = "任务达到 24 小时时限，已保留完成地址"
	}
	checkpoint()
}
