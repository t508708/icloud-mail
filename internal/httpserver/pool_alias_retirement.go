package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"icloud-api/internal/hmesync"
	"icloud-api/internal/store"
)

const poolAliasRetirementJobTimeout = 2 * time.Hour

type poolAliasRetirementJobRuntime struct {
	mu       sync.Mutex
	ctx      context.Context
	active   map[string]struct{}
	stopping bool
	wg       sync.WaitGroup
}

type poolAliasRetirementRequest struct {
	OperationID string                               `json:"operation_id"`
	Items       []store.PoolAliasRetirementItemInput `json:"items"`
}

func (s *Server) registerPoolAliasRetirementRoutes(api *gin.RouterGroup) {
	api.Use(s.poolAuth())
	api.POST("/aliases/batch", s.adminAPIPoolAliasRetirementStart)
	api.GET("/aliases/batch/jobs/:operationID", s.adminAPIPoolAliasRetirementGet)
}

// StartPoolAliasRetirementJobs marks abandoned work interrupted before this
// process accepts jobs. It intentionally does not replay remote mutations.
func (s *Server) StartPoolAliasRetirementJobs(ctx context.Context) error {
	runtime := &s.poolAliasRetirementJobs
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.ctx != nil {
		return errors.New("pool alias retirement jobs already started")
	}
	if err := s.store.InterruptPoolAliasRetirementJobs(ctx); err != nil {
		return err
	}
	runtime.ctx = ctx
	runtime.active = make(map[string]struct{})
	return nil
}

func (s *Server) RunPoolAliasRetirementJobs() {
	runtime := &s.poolAliasRetirementJobs
	runtime.mu.Lock()
	ctx := runtime.ctx
	runtime.mu.Unlock()
	if ctx == nil {
		return
	}
	<-ctx.Done()
	runtime.mu.Lock()
	runtime.stopping = true
	runtime.mu.Unlock()
	runtime.wg.Wait()
}

func (s *Server) adminAPIPoolAliasRetirementStart(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var input poolAliasRetirementRequest
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	client := poolClient(c)
	// Existing jobs are read before current aliases: a completed job has
	// deliberately removed them, while a replay must remain observable.
	if existing, err := s.store.GetPoolAliasRetirementJob(c.Request.Context(), input.OperationID, client.ID); err == nil {
		if !poolAliasRetirementItemsMatch(existing, input.Items) {
			writeAdminAPIError(c, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "operation_id 已用于不同的删除项目")
			return
		}
		writeAdminAPIData(c, http.StatusAccepted, poolAliasRetirementDTO(existing))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.writeAdminAPIInternalError(c, err)
		return
	}
	if err := store.ValidatePoolAliasRetirementInput(input.OperationID, input.Items); err != nil {
		s.poolAliasRetirementError(c, err)
		return
	}
	if s.hmeSync == nil {
		writeAdminAPIError(c, http.StatusServiceUnavailable, "APPLE_HME_UNAVAILABLE", "Apple 隐私邮箱删除服务暂不可用")
		return
	}

	accounts, err := s.store.PoolAliasRetirementAccounts(c.Request.Context(), client.ID, input.Items)
	if err != nil {
		s.poolAliasRetirementError(c, err)
		return
	}
	for _, accountID := range accounts {
		info, err := s.hmeSync.GetSession(c.Request.Context(), accountID)
		if err != nil {
			s.poolAliasRetirementApplePreflightError(c, err)
			return
		}
		if info.Status == hmesync.StatusExpired {
			s.poolAliasRetirementApplePreflightError(c, hmesync.ErrSessionExpired)
			return
		}
		if info.Status != hmesync.StatusAuthenticated {
			s.poolAliasRetirementApplePreflightError(c, hmesync.ErrLoginRequired)
			return
		}
	}

	runtime := &s.poolAliasRetirementJobs
	runtime.mu.Lock()
	if runtime.ctx == nil || runtime.stopping || runtime.ctx.Err() != nil {
		runtime.mu.Unlock()
		writeAdminAPIError(c, http.StatusServiceUnavailable, "POOL_RETIREMENT_UNAVAILABLE", "后台删除服务尚未启动或正在关闭")
		return
	}
	job, accepted, err := s.store.StartPoolAliasRetirement(c.Request.Context(), client.ID, input.OperationID, requestID(c), input.Items)
	if err != nil {
		runtime.mu.Unlock()
		s.poolAliasRetirementError(c, err)
		return
	}
	if !accepted {
		runtime.mu.Unlock()
		writeAdminAPIData(c, http.StatusAccepted, poolAliasRetirementDTO(job))
		return
	}
	runtime.active[job.OperationID] = struct{}{}
	runtime.wg.Add(1)
	workerContext := runtime.ctx
	runtime.mu.Unlock()
	go func() {
		defer func() {
			runtime.mu.Lock()
			delete(runtime.active, job.OperationID)
			runtime.mu.Unlock()
			runtime.wg.Done()
		}()
		s.runPoolAliasRetirementJob(workerContext, client, job)
	}()
	writeAdminAPIData(c, http.StatusAccepted, poolAliasRetirementDTO(job))
}

func (s *Server) adminAPIPoolAliasRetirementGet(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	job, err := s.store.GetPoolAliasRetirementJob(c.Request.Context(), c.Param("operationID"), poolClient(c).ID)
	if errors.Is(err, store.ErrNotFound) {
		writeAdminAPIError(c, http.StatusNotFound, "NOT_FOUND", "删除任务不存在")
		return
	}
	if err != nil {
		s.writeAdminAPIInternalError(c, err)
		return
	}
	writeAdminAPIData(c, http.StatusOK, poolAliasRetirementDTO(job))
}

func poolAliasRetirementItemsMatch(job store.PoolAliasRetirementJob, items []store.PoolAliasRetirementItemInput) bool {
	if len(job.Items) != len(items) {
		return false
	}
	for index, item := range job.Items {
		if item.AliasID != items[index].AliasID || item.LeaseID != strings.TrimSpace(items[index].LeaseID) {
			return false
		}
	}
	return true
}

func poolAliasRetirementDTO(job store.PoolAliasRetirementJob) gin.H {
	items := make([]gin.H, 0, len(job.Items))
	processed := 0
	for _, item := range job.Items {
		if item.State != "retiring" {
			processed++
		}
		items = append(items, gin.H{
			"alias_id": item.AliasID, "lease_id": item.LeaseID, "state": item.State, "result_code": item.ResultCode,
			"result_message": item.ResultMessage,
		})
	}
	return gin.H{
		"operation_id": job.OperationID, "status": job.Status,
		"total": len(job.Items), "processed": processed, "items": items,
		"created_at": job.CreatedAt.UTC().Format(time.RFC3339Nano), "updated_at": job.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func (s *Server) poolAliasRetirementError(c *gin.Context, err error) {
	status, code, message := http.StatusConflict, "POOL_RETIREMENT_CONFLICT", "邮箱或租约状态已变化，请刷新后重试"
	switch {
	case errors.Is(err, store.ErrPoolInput):
		status, code, message = http.StatusBadRequest, "INVALID_POOL_REQUEST", "operation_id 或删除项目无效"
	case errors.Is(err, store.ErrPoolRequestConflict):
		code, message = "IDEMPOTENCY_CONFLICT", "operation_id 已用于不同的删除项目"
	case errors.Is(err, store.ErrPoolRetirementLeaseNotFound):
		code, message = "LEASE_NOT_FOUND", "指定租约不存在"
	case errors.Is(err, store.ErrPoolRetirementProject):
		code, message = "LEASE_PROJECT_MISMATCH", "租约不属于当前项目"
	case errors.Is(err, store.ErrPoolRetirementAlias):
		code, message = "LEASE_ALIAS_MISMATCH", "租约与隐藏邮箱不匹配"
	case errors.Is(err, store.ErrPoolRetirementNotUsed):
		code, message = "LEASE_NOT_USED", "仅已使用租约可提交删除"
	case errors.Is(err, store.ErrPoolRetirementNotLive):
		code, message = "LEASE_NOT_LIVE", "租约不是该邮箱唯一有效分配"
	case errors.Is(err, store.ErrPoolRetirementNotICloud):
		code, message = "ALIAS_NOT_ICLOUD", "隐藏邮箱不属于已认证 iCloud 主号"
	case errors.Is(err, store.ErrPoolRetirementPending):
		code, message = "ALIAS_PENDING_CONFIRMATION", "隐藏邮箱目录确认尚未完成"
	case errors.Is(err, store.ErrPoolClosed):
		code, message = "LEASE_CLOSED", "项目 Key 已停用或租约已关闭"
	default:
		s.logger.Error("邮箱池退休删除预检失败", "error", err, "request_id", requestID(c))
	}
	writeAdminAPIError(c, status, code, message)
}

func (s *Server) poolAliasRetirementApplePreflightError(c *gin.Context, err error) {
	code := hmesync.Code(err)
	if code == "" {
		switch {
		case errors.Is(err, hmesync.ErrSessionExpired):
			code = hmesync.CodeSessionExpired
		case errors.Is(err, hmesync.ErrLoginRequired):
			code = hmesync.CodeLoginRequired
		}
	}
	if code == "" {
		code = "APPLE_DELETE_UNAVAILABLE"
	}
	message := "Apple 会话或删除服务当前不可用"
	if code == hmesync.CodeSessionExpired || code == hmesync.CodeLoginRequired {
		message = "Apple 会话已过期，请重新登录后再提交删除"
	}
	writeAdminAPIError(c, http.StatusConflict, code, message)
}

func (s *Server) runPoolAliasRetirementJob(parent context.Context, client store.PoolClient, job store.PoolAliasRetirementJob) {
	ctx, cancel := context.WithTimeout(parent, poolAliasRetirementJobTimeout)
	defer cancel()
	finished := false
	defer func() {
		if recover() != nil {
			s.logger.Error("邮箱池退休删除任务异常中断", "operation_id", job.OperationID, "request_id", job.RequestID)
		}
		if !finished {
			s.interruptPoolAliasRetirementJob(job, client, "JOB_INTERRUPTED", "删除任务中断，Apple 结果待人工核对")
		}
	}()
	if err := s.store.SetPoolAliasRetirementJobStatus(ctx, job.OperationID, client.ID, store.PoolAliasRetirementJobRunning); err != nil {
		return
	}

	byAlias := make(map[int64]int, len(job.Items))
	ids := make([]int64, 0, len(job.Items))
	for _, item := range job.Items {
		byAlias[item.AliasID] = item.Ordinal
		ids = append(ids, item.AliasID)
	}
	seen := make(map[int64]bool, len(job.Items))
	record := func(outcome hmesync.AliasDeletionOutcome) {
		ordinal, exists := byAlias[outcome.AliasID]
		if !exists || seen[outcome.AliasID] {
			return
		}
		seen[outcome.AliasID] = true
		if outcome.Err == nil {
			if err := s.store.FinalizePoolAliasRetirement(context.Background(), job.OperationID, ordinal, outcome.AliasID); err == nil {
				return
			}
			s.markPoolAliasRetirementReview(job, ordinal, "APPLE_DELETE_UNCERTAIN", "Apple 删除已确认但本地收口未完成，需要人工核对")
			return
		}
		code := poolAliasRetirementOutcomeCode(outcome.Err)
		if poolAliasRetirementOutcomeUnknown(outcome.Err) {
			s.markPoolAliasRetirementReview(job, ordinal, code, "Apple 删除结果未确认，需要人工核对")
			return
		}
		if err := s.store.RestorePoolAliasRetirement(context.Background(), job.OperationID, ordinal, code, "Apple 未删除该隐藏邮箱，租约已恢复为已使用"); err != nil {
			s.markPoolAliasRetirementReview(job, ordinal, "APPLE_DELETE_UNCERTAIN", "删除失败后的租约恢复未完成，需要人工核对")
		}
	}
	// Service.DeleteAliases holds the primary-account lock while invoking its
	// progress callback. Keep Pool state writes outside that callback: Pool
	// mutations lock Pool before account rows, whereas the Apple executor holds
	// an account lock. A no-op finalizer prevents its default local delete;
	// outcomes are published below after all account locks have been released.
	ctx = hmesync.WithAliasDeletionRecovery(ctx, nil)
	ctx = hmesync.WithAliasDeletionFinalizer(ctx, func(context.Context, int64) error { return nil })

	var outcomes []hmesync.AliasDeletionOutcome
	var runErr error
	if batch, ok := s.hmeSync.(HMEBatchDeletionService); ok {
		outcomes, runErr = batch.DeleteAliases(ctx, ids)
	} else {
		for _, aliasID := range ids {
			if err := ctx.Err(); err != nil {
				runErr = err
				break
			}
			err := s.hmeSync.DeleteAlias(ctx, aliasID)
			record(hmesync.AliasDeletionOutcome{AliasID: aliasID, Err: err})
		}
	}
	for _, outcome := range outcomes {
		record(outcome)
	}
	if runErr != nil || ctx.Err() != nil {
		s.interruptPoolAliasRetirementJob(job, client, "JOB_INTERRUPTED", "删除任务中断，Apple 结果待人工核对")
		finished = true
		return
	}
	final, err := s.store.GetPoolAliasRetirementJob(context.Background(), job.OperationID, client.ID)
	if err != nil {
		return
	}
	status := store.PoolAliasRetirementJobCompleted
	for _, item := range final.Items {
		if item.State == "retiring" || item.State == "review" {
			status = store.PoolAliasRetirementJobReview
			break
		}
	}
	if err := s.store.SetPoolAliasRetirementJobStatus(context.Background(), job.OperationID, client.ID, status); err != nil {
		return
	}
	finished = true
}

func (s *Server) interruptPoolAliasRetirementJob(job store.PoolAliasRetirementJob, client store.PoolClient, code, message string) {
	current, err := s.store.GetPoolAliasRetirementJob(context.Background(), job.OperationID, client.ID)
	if err != nil {
		return
	}
	for _, item := range current.Items {
		if item.State == "retiring" {
			_ = s.store.MarkPoolAliasRetirementReview(context.Background(), job.OperationID, item.Ordinal, code, message)
		}
	}
	_ = s.store.SetPoolAliasRetirementJobStatus(context.Background(), job.OperationID, client.ID, store.PoolAliasRetirementJobInterrupted)
}

func (s *Server) markPoolAliasRetirementReview(job store.PoolAliasRetirementJob, ordinal int, code, message string) {
	if err := s.store.MarkPoolAliasRetirementReview(context.Background(), job.OperationID, ordinal, code, message); err != nil {
		s.logger.Error("保存邮箱池退休删除待核查状态失败", "operation_id", job.OperationID, "ordinal", ordinal, "request_id", job.RequestID)
	}
}

func poolAliasRetirementOutcomeUnknown(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, hmesync.ErrUpstream) || errors.Is(err, hmesync.ErrPersistence) || errors.Is(err, hmesync.ErrCrypto) ||
		errors.Is(err, hmesync.ErrRateLimited) || errors.Is(err, hmesync.ErrBatchDeferred) {
		return true
	}
	switch hmesync.Code(err) {
	case hmesync.CodeUpstreamError, hmesync.CodePersistenceError, hmesync.CodeCryptoError,
		hmesync.CodeRateLimited, hmesync.CodeBatchDeferred, hmesync.CodeSessionExpired, hmesync.CodeLoginRequired:
		return true
	default:
		return false
	}
}

func poolAliasRetirementOutcomeCode(err error) string {
	if code := hmesync.Code(err); code != "" {
		return code
	}
	switch {
	case errors.Is(err, hmesync.ErrRateLimited):
		return hmesync.CodeRateLimited
	case errors.Is(err, hmesync.ErrBatchDeferred):
		return hmesync.CodeBatchDeferred
	case errors.Is(err, hmesync.ErrSessionExpired):
		return hmesync.CodeSessionExpired
	case errors.Is(err, hmesync.ErrLoginRequired):
		return hmesync.CodeLoginRequired
	case poolAliasRetirementOutcomeUnknown(err):
		return "APPLE_DELETE_UNCERTAIN"
	default:
		return "APPLE_DELETE_FAILED"
	}
}
