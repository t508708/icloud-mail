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
	clientID := c.Param("clientID")
	page, err := s.store.ListPoolProjectAliases(c.Request.Context(), clientID, accountID, limit, offset)
	if err != nil {
		s.poolError(c, err)
		return
	}
	type projectMailboxDTO struct {
		store.PoolProjectAlias
		OTPURLPath        string `json:"otp_url_path"`
		ProjectOTPURLPath string `json:"project_otp_url_path,omitempty"`
		InboxSharePath    string `json:"inbox_share_path,omitempty"`
	}
	items := make([]projectMailboxDTO, 0, len(page.Items))
	for _, link := range page.Items {
		apiKeyHash := link.APIKeyHash
		if len(apiKeyHash) == 0 {
			alias, lookupErr := s.store.GetAlias(c.Request.Context(), link.AliasID)
			if lookupErr != nil {
				s.poolError(c, lookupErr)
				return
			}
			apiKeyHash = alias.APIKeyHash
		}
		token, err := s.cipher.OTPToken(link.AliasID, apiKeyHash)
		if err != nil {
			s.writeAdminAPIInternalError(c, err)
			return
		}
		path := (&url.URL{Path: "/api/v1/otp", RawQuery: (url.Values{"token": {token}}).Encode()}).String()
		projectPath := ""
		if projectToken, projErr := s.cipher.ProjectOTPToken(link.AliasID, apiKeyHash, clientID); projErr == nil {
			projectPath = (&url.URL{Path: "/api/v1/otp", RawQuery: (url.Values{"token": {projectToken}}).Encode()}).String()
		}
		inboxPath := ""
		if inboxToken, inboxErr := s.cipher.InboxToken(link.AliasID, apiKeyHash); inboxErr == nil {
			inboxPath = (&url.URL{Path: "/inbox/view", RawQuery: (url.Values{"token": {inboxToken}}).Encode()}).String()
		}
		items = append(items, projectMailboxDTO{
			PoolProjectAlias:  link,
			OTPURLPath:        path,
			ProjectOTPURLPath: projectPath,
			InboxSharePath:    inboxPath,
		})
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
