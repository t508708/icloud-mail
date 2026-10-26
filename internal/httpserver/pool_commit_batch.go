package httpserver

import "github.com/gin-gonic/gin"

func (s *Server) poolCommitLeases(c *gin.Context) {
	var input struct {
		LeaseIDs []string `json:"lease_ids"`
	}
	if !decodeAdminAPIJSON(c, &input) {
		return
	}
	client := poolClient(c)
	leases, err := s.store.CommitPoolLeases(c.Request.Context(), client.ID, input.LeaseIDs)
	if err != nil {
		s.poolError(c, err)
		return
	}
	for _, lease := range leases {
		s.audit(c, nil, client.Project, "pool_commit", "pool_lease", lease.ID, "success", "")
	}
	c.JSON(200, gin.H{"data": gin.H{"leases": leases}})
}
