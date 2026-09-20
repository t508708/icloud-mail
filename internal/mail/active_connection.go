package mail

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"

	imapclient "github.com/emersion/go-imap/v2/imapclient"

	"icloud-api/internal/domain"
)

type activeConnectionKey struct{}

// One receiver worker owns one read connection. Its lifetime, rather than an
// HTTP request, controls the socket; there is no unbounded global connection pool.
type activeConnection struct {
	ctx       context.Context
	accountID int64
	slot      chan struct{}
	mu        sync.Mutex
	client    *imapclient.Client
	identity  [32]byte
	closed    bool
}

func (f *Fetcher) OpenActiveAccount(ctx context.Context, accountID int64) (context.Context, func()) {
	lifetime, cancel := context.WithCancel(ctx)
	session := &activeConnection{ctx: lifetime, accountID: accountID, slot: make(chan struct{}, 1)}
	stop := context.AfterFunc(lifetime, session.close)
	return context.WithValue(lifetime, activeConnectionKey{}, session), func() {
		cancel()
		stop()
		session.close()
	}
}

func (s *activeConnection) close() {
	s.mu.Lock()
	s.closed = true
	client := s.client
	s.client = nil
	s.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

// release(false) discards a failed or cancelled connection. SELECT is still
// repeated on every batch to refresh UIDVALIDITY/UIDNEXT and the mailbox view.
func acquireArchiveConnection(ctx context.Context, accountID int64, address, host, username, password string, settings fetchSettings) (*imapclient.Client, bool, func(bool), error) {
	var session *activeConnection
	if settings.activeRecent {
		session, _ = ctx.Value(activeConnectionKey{}).(*activeConnection)
	}
	if session != nil && session.accountID != accountID {
		return nil, false, nil, errors.New("active IMAP connection account mismatch")
	}
	unlock := func() {}
	var client *imapclient.Client
	identity := sha256.Sum256([]byte(fmt.Sprintf("%q", []string{address, host, username, password})))
	if session != nil {
		select {
		case session.slot <- struct{}{}:
		case <-ctx.Done():
			return nil, false, nil, ctx.Err()
		case <-session.ctx.Done():
			return nil, false, nil, session.ctx.Err()
		}
		unlock = func() { <-session.slot }
		session.mu.Lock()
		if session.closed || ctx.Err() != nil || session.ctx.Err() != nil {
			session.mu.Unlock()
			unlock()
			return nil, false, nil, context.Canceled
		}
		client = session.client
		if session.identity != identity {
			session.client = nil
			if client != nil {
				_ = client.Close()
				client = nil
			}
		}
		session.mu.Unlock()
	}
	reused := client != nil
	if client == nil {
		var err error
		client, err = dialArchiveIMAP(ctx, address, host, settings)
		if err != nil {
			unlock()
			return nil, false, nil, fmt.Errorf("connect IMAP %s: %w", address, err)
		}
	}
	// Join the cancellation callback before returning the connection to its
	// owner, so a late callback cannot close a socket borrowed by the next batch.
	cancelled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = client.Close(); close(cancelled) })
	release := func(healthy bool) {
		if !stop() {
			<-cancelled
			healthy = false
		}
		if session != nil {
			session.mu.Lock()
			healthy = healthy && !session.closed && session.ctx.Err() == nil && ctx.Err() == nil
			if healthy {
				session.client, session.identity = client, identity
			} else if session.client == client {
				session.client = nil
			}
			session.mu.Unlock()
		}
		if !healthy || session == nil {
			_ = client.Close()
		}
		unlock()
	}
	if !reused {
		domain.ReportMailboxSyncProgress(ctx, domain.MailboxSyncPhaseAuthenticating, 10)
		if err := client.Login(username, password).Wait(); err != nil {
			release(false)
			if ctx.Err() != nil {
				return nil, false, nil, ctx.Err()
			}
			return nil, false, nil, fmt.Errorf("login IMAP account: %w", err)
		}
	}
	return client, reused, release, nil
}
