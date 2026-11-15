package httpserver

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/gin-gonic/gin"
	"icloud-api/internal/store"
)

func (s *Server) adminPoolProjectMailboxes(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.DefaultQuery("account_id", "0"), 10, 64)
	if err != nil || accountID < 0 {
		s.poolError(c, store.ErrPoolInput)
		return
	}
	limit, offset := poolPagination(c)
	// A single export page can include all HME addresses of one main account.
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	page, err := s.store.ListPoolProjectAliases(c.Request.Context(), c.Param("clientID"), accountID, limit, offset)
	if err != nil {
		s.poolError(c, err)
		return
	}
	type projectMailboxDTO struct {
		store.PoolProjectAlias
		OTPURLPath string `json:"otp_url_path"`
	}
	items := make([]projectMailboxDTO, 0, len(page.Items))
	for _, link := range page.Items {
		alias, err := s.store.GetAlias(c.Request.Context(), link.AliasID)
		if err != nil {
			s.poolError(c, err)
			return
		}
		token, err := s.cipher.OTPToken(alias.ID, alias.APIKeyHash)
		if err != nil {
			s.writeAdminAPIInternalError(c, err)
			return
		}
		path := (&url.URL{Path: "/api/v1/otp", RawQuery: (url.Values{"token": {token}}).Encode()}).String()
		items = append(items, projectMailboxDTO{PoolProjectAlias: link, OTPURLPath: path})
	}
	c.Header("Cache-Control", "no-store")
	writeAdminAPIData(c, http.StatusOK, gin.H{"items": items, "total": page.Total})
}

func (s *Server) adminLinkPoolProjectMailboxes(c *gin.Context) {
	var input struct {
		AccountID int64 `json:"account_id"`
	}
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	linked, added, err := s.store.LinkPoolProjectAccountAliases(c.Request.Context(), c.Param("clientID"), input.AccountID)
	if err != nil {
		s.poolError(c, err)
		return
	}
	s.audit(c, nil, "admin", "pool_project_share", "account", strconv.FormatInt(input.AccountID, 10), "success", strconv.Itoa(added))
	writeAdminAPIData(c, http.StatusOK, gin.H{"linked": linked, "added": added})
}

func (s *Server) adminUnlinkPoolProjectMailbox(c *gin.Context) {
	aliasID, err := strconv.ParseInt(c.Param("aliasID"), 10, 64)
	if err != nil || aliasID <= 0 {
		s.poolError(c, store.ErrPoolInput)
		return
	}
	if err := s.store.UnlinkPoolProjectAlias(c.Request.Context(), c.Param("clientID"), aliasID); err != nil {
		s.poolError(c, err)
		return
	}
	s.audit(c, nil, "admin", "pool_project_unshare", "alias", strconv.FormatInt(aliasID, 10), "success", "")
	writeAdminAPIData(c, http.StatusOK, gin.H{"saved": true})
}
