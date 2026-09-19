package httpserver

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	aliasDemandMinimumInterval = 2 * time.Second
	pickupConcurrentLimit      = 16
	pickupAccountLimit         = 2
	pickupRateMaxEntries       = 8192
)

type aliasDemandRateState struct {
	inFlight bool
	nextAt   time.Time
}

// Admission runs before credential locks and database authentication. Slots
// bound the entire request, including slow upstream reads, with no wait queue.
func (s *Server) pickupAdmission() gin.HandlerFunc {
	limit := pickupConcurrentLimit
	if s.sharedMailboxReceiver {
		limit = 128
	}
	slots := make(chan struct{}, limit)
	rate := newWindowLimiter(100, time.Second)
	return func(c *gin.Context) {
		switch c.FullPath() {
		case "/api/v1/otp", "/api/v1/mail/latest", "/api/v1/mail/recent", "/api/v1/mail/recent/", "/api/v1/pool/leases/:leaseID/code":
		default:
			c.Next()
			return
		}
		c.Header("Cache-Control", "no-store")
		if !rate.Allow("pickup") {
			s.rejectMailboxPickup(c, "取件请求较多，请在 2 秒后重试")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			s.rejectMailboxPickup(c, "取件服务繁忙，请在 2 秒后重试")
			return
		}
		c.Next()
	}
}

func (s *Server) rejectMailboxPickup(c *gin.Context, message string) {
	c.Header("Retry-After", "2")
	s.writeAPIError(c, http.StatusTooManyRequests, "RATE_LIMITED", message)
	c.Abort()
}

// Call only after authentication. All credentials and routes for the same
// alias share this state, and failed fetches also observe the cooldown.
func (s *Server) beginMailboxPickup(c *gin.Context, accountID, aliasID int64) (func(), bool) {
	now := s.now()
	s.aliasDemandRateMu.Lock()
	if s.aliasDemandRate == nil {
		s.aliasDemandRate = make(map[int64]aliasDemandRateState)
		s.accountPickupActive = make(map[int64]int)
	}
	if !now.Before(s.pickupRateCleanupAt) {
		for id, state := range s.aliasDemandRate {
			if !state.inFlight && !now.Before(state.nextAt) {
				delete(s.aliasDemandRate, id)
			}
		}
		s.pickupRateCleanupAt = now.Add(time.Second)
	}
	state, exists := s.aliasDemandRate[aliasID]
	message := ""
	accountLimit := pickupAccountLimit
	if s.sharedMailboxReceiver {
		accountLimit = 64
	}
	switch {
	case state.inFlight || now.Before(state.nextAt):
		message = "同一邮箱正在取件或距上次完成不足 2 秒，请稍后重试"
	case s.accountPickupActive[accountID] >= accountLimit:
		message = "此主号已有取件请求处理中，请在 2 秒后重试"
	case !exists && len(s.aliasDemandRate) >= pickupRateMaxEntries:
		message = "取件请求较多，请在 2 秒后重试"
	}
	if message != "" {
		s.aliasDemandRateMu.Unlock()
		s.rejectMailboxPickup(c, message)
		return nil, false
	}
	s.aliasDemandRate[aliasID] = aliasDemandRateState{inFlight: true}
	s.accountPickupActive[accountID]++
	s.aliasDemandRateMu.Unlock()
	return func() {
		finished := s.now()
		s.aliasDemandRateMu.Lock()
		defer s.aliasDemandRateMu.Unlock()
		// Rejections above do not mutate the state. The cooldown is anchored to
		// this completed pickup, so repeated refreshes cannot push it forward.
		s.aliasDemandRate[aliasID] = aliasDemandRateState{nextAt: finished.Add(aliasDemandMinimumInterval)}
		s.accountPickupActive[accountID]--
		if s.accountPickupActive[accountID] == 0 {
			delete(s.accountPickupActive, accountID)
		}
	}, true
}
