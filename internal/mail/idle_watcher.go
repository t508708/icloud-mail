package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	imapv2 "github.com/emersion/go-imap/v2"
	imapclientv2 "github.com/emersion/go-imap/v2/imapclient"
	"icloud-api/internal/domain"
)

// ErrIdleUnsupported indicates that the server cannot maintain an IDLE watch.
var ErrIdleUnsupported = errors.New("imap IDLE is unsupported")

// WatchMailbox waits for INBOX EXISTS updates. Reconnect and backoff belong to
// the caller; this function owns exactly one IMAP connection.
func (f *Fetcher) WatchMailbox(ctx context.Context, account domain.Account, password string, notify func()) error {
	settings := f.settings()
	if err := validateIMAPAccount(account, password); err != nil {
		return err
	}
	host, address, username, err := accountEndpoint(account)
	if err != nil {
		return err
	}
	dialer := &net.Dialer{Timeout: settings.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect IMAP: %w", err)
	}
	tlsConn := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	hctx, cancel := context.WithTimeout(ctx, settings.timeout)
	err = tlsConn.HandshakeContext(hctx)
	cancel()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("IMAP TLS handshake: %w", err)
	}
	return watchIMAPConnection(ctx, tlsConn, username, password, settings.timeout, notify)
}

// Notifications are coalesced away from the decoder and delivered serially.
// The callback must return promptly; mailbox reads belong to another worker.
func watchIMAPConnection(ctx context.Context, conn net.Conn, username, password string, timeout time.Duration, notify func()) (resultErr error) {
	watchCtx, cancelWatch := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(watchCtx, func() { _ = conn.Close() })
	setupCtx, cancelSetup := context.WithTimeout(watchCtx, timeout)
	stopSetupTimeout := context.AfterFunc(setupCtx, func() { _ = conn.Close() })
	events := make(chan struct{}, 1)
	enqueue := func() {
		select {
		case events <- struct{}{}:
		default:
		}
	}
	client := imapclientv2.New(conn, &imapclientv2.Options{UnilateralDataHandler: &imapclientv2.UnilateralDataHandler{
		Mailbox: func(data *imapclientv2.UnilateralDataMailbox) {
			if data != nil && data.NumMessages != nil {
				enqueue()
			}
		},
	}})
	var delivery sync.WaitGroup
	setupFinished := false
	defer func() {
		if ctx.Err() != nil {
			resultErr = ctx.Err()
		} else if !setupFinished && setupCtx.Err() != nil {
			resultErr = setupCtx.Err()
		}
		stopSetupTimeout()
		cancelSetup()
		stopCancellation()
		cancelWatch()
		_ = client.Close()
		delivery.Wait()
	}()
	// A context timer bounds all setup commands; the client resets socket read
	// deadlines internally, so setting a deadline on the connection is not enough.
	if err := client.Login(username, password).Wait(); err != nil {
		return fmt.Errorf("login IMAP account: %w", err)
	}
	if _, err := client.Select("INBOX", &imapv2.SelectOptions{ReadOnly: true}).Wait(); err != nil {
		return fmt.Errorf("select INBOX: %w", err)
	}
	if !client.Caps().Has(imapv2.CapIdle) && !client.Caps().Has(imapv2.CapIMAP4rev2) {
		return ErrIdleUnsupported
	}
	idle, err := client.Idle()
	if err != nil {
		return fmt.Errorf("start IMAP IDLE: %w", err)
	}
	if !stopSetupTimeout() || setupCtx.Err() != nil {
		_ = client.Close()
		_ = idle.Close()
		return context.DeadlineExceeded
	}
	setupFinished = true
	cancelSetup()
	enqueue() // Catch up once after startup/reconnect, including the SELECT gap.
	delivery.Add(1)
	go func() {
		defer delivery.Done()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-events:
				if notify != nil && watchCtx.Err() == nil {
					notify()
				}
			}
		}
	}()
	err = idle.Wait()
	if err != nil {
		return fmt.Errorf("IMAP IDLE: %w", err)
	}
	return nil
}
