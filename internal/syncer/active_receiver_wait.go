package syncer

import "time"

// MailboxChanges returns the current account change notification and a
// bounded hint for when another demand sync may be useful. It only observes
// an existing worker; it never creates or wakes one.
func (r *ActiveReceiver) MailboxChanges(accountID int64) (<-chan struct{}, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, 0
	}
	w := r.workers[accountID]
	if w == nil || w.closed || w.ctx.Err() != nil {
		return nil, 0
	}
	changed := w.changed
	now := time.Now()
	if w.err != nil {
		return changed, max(w.nextTry.Sub(now), 0)
	}
	if w.generation != w.committed {
		return changed, r.manager.syncTimeout
	}
	freshness := r.fallbackFreshness
	if w.connected {
		freshness = r.freshness
	}
	return changed, max(w.lastSync.Add(freshness).Sub(now), 0)
}
