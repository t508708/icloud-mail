package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"icloud-api/internal/apple"
	"icloud-api/internal/domain"
	"icloud-api/internal/hmesync"
	"icloud-api/internal/store"
)

// Optional for embedders; the production HME service already implements this.
type manualAliasCreator interface {
	CreateAutoAlias(context.Context, int64) (domain.Alias, error)
}

func (s *Server) adminAPICreateAppleAlias(c *gin.Context) {
	accountID, ok := adminAPIParseID(c)
	if !ok {
		return
	}
	var input struct{}
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	account, err := s.store.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		s.writeAdminAPIStoreReadError(c, err)
		return
	}
	if domain.NormalizeMailboxType(account.MailboxType) != domain.MailboxTypeICloud {
		writeAdminAPIError(c, http.StatusConflict, "CUSTOM_MAILBOX_NO_APPLE", "请选择 iCloud 主号创建隐藏邮箱")
		return
	}
	if !account.Enabled {
		writeAdminAPIError(c, http.StatusConflict, "ACCOUNT_DISABLED", "请先启用主号")
		return
	}
	creator, ok := s.hmeSync.(manualAliasCreator)
	if !ok {
		s.adminAPIFinishAppleFailure(c, mustSession(c), accountID, "alias_create_manual", adminAPIAppleServiceUnavailable())
		return
	}
	// Reject concurrent manual clicks instead of queuing another remote reserve.
	s.manualAliasMu.Lock()
	if s.manualAliasesRunning[accountID] {
		s.manualAliasMu.Unlock()
		writeAdminAPIError(c, http.StatusConflict, "ALIAS_CREATION_BUSY", "这个主号正在手动创建，请等待当前操作完成")
		return
	}
	if s.manualAliasesRunning == nil {
		s.manualAliasesRunning = make(map[int64]bool)
	}
	s.manualAliasesRunning[accountID] = true
	s.manualAliasMu.Unlock()
	defer func() {
		s.manualAliasMu.Lock()
		delete(s.manualAliasesRunning, accountID)
		s.manualAliasMu.Unlock()
	}()

	// The HME service coordinates with automatic creation using the same account
	// locks and persists uncertain reserve results for later confirmation.
	ctx, cancel := context.WithTimeout(c.Request.Context(), 90*time.Second)
	defer cancel()
	alias, err := creator.CreateAutoAlias(ctx, accountID)
	if err != nil {
		apiErr := classifyManualAliasError(err)
		if delay := apple.RetryDelay(err); delay > 0 {
			c.Header("Retry-After", strconv.FormatInt(int64((delay+time.Second-1)/time.Second), 10))
		}
		s.adminAPIFinishAppleFailure(c, mustSession(c), accountID, "alias_create_manual", apiErr)
		return
	}
	if alias.ID < 1 || alias.AccountID != accountID || !alias.Enabled || adminAPIAliasConfirmationPending(alias) {
		s.adminAPIFinishAppleFailure(c, mustSession(c), accountID, "alias_create_manual", adminAPIAppleError{
			Status: http.StatusConflict, Code: hmesync.CodeAliasConfirmationPending,
			Message: "创建结果待确认，请先刷新邮箱列表查看结果",
		})
		return
	}
	dto, err := s.adminAPIAliasFromDomain(alias)
	if err != nil {
		// A created mailbox remains available in the list even if serializing its
		// credentials fails. Do not encourage repeating this side effect.
		s.adminAPIFinishAppleFailure(c, mustSession(c), accountID, "alias_create_manual", adminAPIAppleError{
			Status: http.StatusInternalServerError, Code: "ALIAS_CREATED_DETAILS_PENDING",
			Message: "邮箱已创建，请刷新邮箱列表获取凭据",
		})
		return
	}
	session := mustSession(c)
	s.audit(c, &session.AdminID, session.Username, "alias_create_manual", "alias", strconv.FormatInt(alias.ID, 10), "success", "")
	c.Header("Cache-Control", "no-store")
	c.Header("Location", fmt.Sprintf("%s/aliases/%d", s.cfg.AdminPath+"/api/v1", alias.ID))
	writeAdminAPIData(c, http.StatusCreated, gin.H{"alias": dto})
}

func classifyManualAliasError(err error) adminAPIAppleError {
	switch {
	case errors.Is(err, store.ErrAliasLimit):
		return adminAPIAppleError{Status: http.StatusConflict, Code: "ALIAS_LIMIT_REACHED", Message: "该主号已达到本地邮箱容量上限"}
	case errors.Is(err, hmesync.ErrAccountDisabled):
		return adminAPIAppleError{Status: http.StatusConflict, Code: hmesync.CodeAccountDisabled, Message: "主号已停用"}
	case errors.Is(err, hmesync.ErrAliasConfirmationPending):
		return adminAPIAppleError{Status: http.StatusConflict, Code: hmesync.CodeAliasConfirmationPending, Message: "Apple 创建结果待目录确认，本轮已停止；请先刷新列表，后续创建会优先确认已有结果"}
	case errors.Is(err, hmesync.ErrForwardingTargetMissing):
		return adminAPIAppleError{Status: http.StatusConflict, Code: hmesync.CodeForwardingTargetMissing, Message: "请先在 Apple 配置隐藏邮箱的默认转发目标"}
	default:
		return classifyAdminAPIAppleError(err)
	}
}
