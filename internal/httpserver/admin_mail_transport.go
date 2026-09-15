package httpserver

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"icloud-api/internal/hmesync"
	"icloud-api/internal/store"
)

func (s *Server) adminAPISetMailTransport(c *gin.Context) {
	id, ok := adminAPIParseID(c)
	if !ok {
		return
	}
	var input struct {
		Transport string `json:"transport"`
	}
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	if input.Transport != store.MailTransportIMAP && input.Transport != store.MailTransportWebmail {
		writeAdminAPIError(c, http.StatusBadRequest, "VALIDATION_FAILED", "请选择 IMAP 或 iCloud 网页收件")
		return
	}
	if input.Transport == store.MailTransportWebmail && !s.cfg.MailOnDemandOnly {
		writeAdminAPIError(c, http.StatusConflict, "MAIL_ON_DEMAND_REQUIRED", "网页收件需要启用按需取件模式")
		return
	}
	err := s.withAccountLock(c.Request.Context(), id, func() error {
		if input.Transport == store.MailTransportWebmail {
			if s.hmeSync == nil {
				return hmesync.ErrLoginRequired
			}
			info, err := s.hmeSync.GetSession(c.Request.Context(), id)
			if err != nil {
				return err
			}
			if info.Status != hmesync.StatusAuthenticated {
				return hmesync.ErrLoginRequired
			}
		}
		return s.store.SetAccountMailTransport(c.Request.Context(), id, input.Transport)
	})
	if err != nil {
		switch {
		case errors.Is(err, hmesync.ErrLoginRequired):
			writeAdminAPIError(c, http.StatusConflict, "WEB_MAIL_LOGIN_REQUIRED", "请先登录 iCloud Web 目录连接，再选择网页收件")
		case errors.Is(err, store.ErrAccountDisabled):
			writeAdminAPIError(c, http.StatusConflict, "ACCOUNT_DISABLED", "请先启用主号")
		case errors.Is(err, store.ErrICloudMailboxRequired):
			writeAdminAPIError(c, http.StatusConflict, "ICLOUD_MAILBOX_REQUIRED", "网页收件适用于直接收件的 iCloud 主号")
		default:
			s.writeAdminAPIStoreReadError(c, err)
		}
		return
	}
	admin := mustSession(c)
	s.audit(c, &admin.AdminID, admin.Username, "set_mail_transport", "account", strconv.FormatInt(id, 10), "success", input.Transport)
	writeAdminAPIData(c, http.StatusOK, gin.H{"transport": input.Transport})
}
