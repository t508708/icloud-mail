package httpserver

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	aliasDemandMinimumInterval = 2 * time.Second
	pickupConcurrentLimit      = 16
	pickupAccountLimit         = 2
	pickupRateMaxEntries       = 8192
	sharedPickupBurst          = 2
)

type aliasDemandRateState struct {
	inFlight bool
	nextAt   time.Time
	waiting  bool
	credit   time.Duration
	refilled time.Time
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
		if retry := rate.allowAt("pickup", s.now()); retry > 0 {
			s.rejectMailboxPickup(c, "global_rate", "取件请求较多，请按 Retry-After 重试", retry)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			s.rejectMailboxPickup(c, "global_concurrency", "取件服务繁忙，请稍后重试", time.Second)
			return
		}
		c.Next()
	}
}

func (s *Server) rejectMailboxPickup(c *gin.Context, reason, message string, retry time.Duration) {
	retry = max(retry, time.Millisecond)
	c.Header("Retry-After", strconv.FormatInt(int64((retry+time.Second-1)/time.Second), 10))
	c.Header("X-Retry-After-Ms", strconv.FormatInt(int64((retry+time.Millisecond-1)/time.Millisecond), 10))
	c.Header("X-RateLimit-Reason", reason)
	c.Set("pickup_limit_reason", reason)
	s.writeAPIError(c, http.StatusTooManyRequests, "RATE_LIMITED", message)
	c.Abort()
}

// Call only after authentication. Shared receivers separate HTTP burst credit
// from upstream work; legacy fetches retain their completion-based cooldown.
func (s *Server) beginMailboxPickup(c *gin.Context, accountID, aliasID int64) (func(), bool) {
	return s.beginMailboxPickupMode(c, accountID, aliasID, false)
}

func (s *Server) beginMailboxPickupMode(c *gin.Context, accountID, aliasID int64, waiting bool) (func(), bool) {
	now := s.now()
	s.aliasDemandRateMu.Lock()
	if s.aliasDemandRate == nil {
		s.aliasDemandRate = make(map[int64]aliasDemandRateState)
		s.accountPickupActive = make(map[int64]int)
	}
	if !now.Before(s.pickupRateCleanupAt) {
		for id, state := range s.aliasDemandRate {
			if !state.inFlight && !state.waiting && !now.Before(state.nextAt) {
				delete(s.aliasDemandRate, id)
			}
		}
		s.pickupRateCleanupAt = now.Add(time.Second)
	}
	state, exists := s.aliasDemandRate[aliasID]
	if s.sharedMailboxReceiver {
		return s.beginSharedMailboxPickupLocked(c, accountID, aliasID, waiting, now, state, exists)
	}
	message := ""
	reason := ""
	retry := aliasDemandMinimumInterval
	accountLimit := pickupAccountLimit
	switch {
	case state.inFlight:
		reason, message = "mailbox_inflight", "同一邮箱正在取件，请等待本次请求完成"
	case now.Before(state.nextAt):
		reason, message = "mailbox_cooldown", "此邮箱取件冷却中，请按剩余时间重试"
		retry = state.nextAt.Sub(now)
	case s.accountPickupActive[accountID] >= accountLimit:
		reason, message = "account_concurrency", "此主号取件请求已满，请稍后重试"
	case !exists && len(s.aliasDemandRate) >= pickupRateMaxEntries:
		reason, message = "mailbox_capacity", "取件邮箱较多，请稍后重试"
	}
	if message != "" {
		s.aliasDemandRateMu.Unlock()
		s.rejectMailboxPickup(c, reason, message, retry)
		return nil, false
	}
	s.aliasDemandRate[aliasID] = aliasDemandRateState{inFlight: true}
	s.accountPickupActive[accountID]++
	s.aliasDemandRateMu.Unlock()
	return sync.OnceFunc(func() {
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
	}), true
}

// Called with aliasDemandRateMu held; every path releases it. Rejected attempts
// never spend credit or change its refill anchor. Only admissions mutate it.
func (s *Server) beginSharedMailboxPickupLocked(c *gin.Context, accountID, aliasID int64, waiting bool, now time.Time, state aliasDemandRateState, exists bool) (func(), bool) {
	const capacity = sharedPickupBurst * aliasDemandMinimumInterval
	if !exists {
		state.credit, state.refilled = capacity, now
	}
	if now.After(state.refilled) {
		state.credit = min(capacity, state.credit+now.Sub(state.refilled))
		state.refilled = now
	}
	reason, message := "", ""
	retry := time.Second
	switch {
	case waiting && state.waiting:
		reason, message = "mailbox_waiting", "此邮箱已有等待新码请求，普通读取仍可使用"
	case !waiting && state.inFlight:
		reason, message = "mailbox_inflight", "此邮箱已有普通取件请求，请等待其完成"
	case s.accountPickupActive[accountID] >= 64:
		reason, message = "account_concurrency", "此主号取件请求已满，请稍后重试"
	case !exists && len(s.aliasDemandRate) >= pickupRateMaxEntries:
		reason, message = "mailbox_capacity", "取件邮箱较多，请稍后重试"
	case state.credit < aliasDemandMinimumInterval:
		reason, message = "mailbox_rate", "此邮箱短时刷新较多，请按剩余时间重试"
		retry = aliasDemandMinimumInterval - state.credit
	}
	if reason != "" {
		s.aliasDemandRateMu.Unlock()
		s.rejectMailboxPickup(c, reason, message, retry)
		return nil, false
	}
	state.credit -= aliasDemandMinimumInterval
	state.nextAt = state.refilled.Add(capacity - state.credit)
	if waiting {
		state.waiting = true
	} else {
		state.inFlight = true
	}
	s.aliasDemandRate[aliasID] = state
	s.accountPickupActive[accountID]++
	s.aliasDemandRateMu.Unlock()
	return sync.OnceFunc(func() {
		s.aliasDemandRateMu.Lock()
		defer s.aliasDemandRateMu.Unlock()
		current := s.aliasDemandRate[aliasID]
		if waiting {
			current.waiting = false
		} else {
			current.inFlight = false
		}
		// Waiting, failures and cancellation do not restart a completion timer.
		s.aliasDemandRate[aliasID] = current
		s.accountPickupActive[accountID]--
		if s.accountPickupActive[accountID] == 0 {
			delete(s.accountPickupActive, accountID)
		}
	}), true
}
