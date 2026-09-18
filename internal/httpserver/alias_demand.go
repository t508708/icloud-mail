package httpserver

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"icloud-api/internal/domain"
	"icloud-api/internal/secure"
)

// Demand reads wait for one bounded target-alias fetch. Reloading the binding
// prevents the same response from using pre-fetch mail or revoked credentials.
func (s *Server) refreshDemandMailbox(c *gin.Context, binding domain.MailboxBinding) (domain.MailboxBinding, bool) {
	if !binding.Alias.Enabled || !binding.Account.Enabled {
		s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "邮箱凭据已失效")
		return domain.MailboxBinding{}, false
	}
	if s.demandAliasSync == nil {
		s.requestMailboxSync(binding.Account.ID, s.now())
		return binding, true
	}
	if err := s.demandAliasSync(c.Request.Context(), binding.Alias.ID); err != nil {
		c.Header("Retry-After", "2")
		s.writeAPIError(c, http.StatusServiceUnavailable, "SYNC_UNAVAILABLE", "本次按需取件尚未完成，请稍后刷新取件地址")
		return domain.MailboxBinding{}, false
	}
	current, err := s.store.GetMailboxBindingByAPIKeyHash(c.Request.Context(), binding.Alias.APIKeyHash)
	if err != nil || !current.Alias.Enabled || !current.Account.Enabled || current.Alias.ID != binding.Alias.ID || !secure.HashEqual(current.Alias.APIKeyHash, binding.Alias.APIKeyHash) {
		s.writeAPIError(c, http.StatusUnauthorized, "INVALID_API_KEY", "邮箱凭据已更新或邮箱已停用")
		return domain.MailboxBinding{}, false
	}
	return current, true
}
