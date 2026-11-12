package httpserver

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// registerPoolClaimableAccountRoutes is separate from the administrator pool
// screen: this narrow read requires both an administrator session and a Pool
// Key, and it intentionally does not disclose account emails or credentials.
func (s *Server) registerPoolClaimableAccountRoutes(api *gin.RouterGroup) {
	api.Use(s.poolAuth())
	api.GET("/accounts/available", s.adminAPIPoolClaimableAccounts)
}

func (s *Server) adminAPIPoolClaimableAccounts(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	accounts, err := s.store.ListPoolClaimableAccounts(c.Request.Context())
	if err != nil {
		s.poolError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": accounts})
}
