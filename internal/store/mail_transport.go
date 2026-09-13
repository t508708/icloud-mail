package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"icloud-api/internal/domain"
)

const (
	MailTransportIMAP    = "imap"
	MailTransportWebmail = "webmail"
)

var ErrInvalidMailTransport = errors.New("invalid mail transport")

func migrateMailTransport(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS account_mail_transports (
  account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  transport TEXT NOT NULL CHECK(transport IN ('imap', 'webmail')),
  updated_at BIGINT NOT NULL
)`)
	return err
}

func (s *Store) GetAccountMailTransport(ctx context.Context, accountID int64) (string, error) {
	var transport string
	err := s.queryRowContext(ctx, `SELECT transport FROM account_mail_transports WHERE account_id = ?`, accountID).Scan(&transport)
	if err == sql.ErrNoRows {
		if _, err := s.GetAccount(ctx, accountID); err != nil {
			return "", err
		}
		return MailTransportIMAP, nil
	}
	return transport, err
}

func (s *Store) SetAccountMailTransport(ctx context.Context, accountID int64, transport string) error {
	if transport != MailTransportIMAP && transport != MailTransportWebmail {
		return ErrInvalidMailTransport
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin mail transport update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.lockAccountVersionForUpdate(ctx, tx, accountID); err != nil {
		return err
	}
	state, err := s.readAccountMailboxStateTx(ctx, tx, accountID)
	if err != nil {
		return err
	}
	if !state.Enabled {
		return ErrAccountDisabled
	}
	if transport == MailTransportWebmail {
		var host string
		var port int
		if err := s.txQueryRowContext(ctx, tx, `SELECT imap_host, imap_port FROM accounts WHERE id = ?`, accountID).Scan(&host, &port); err != nil {
			return err
		}
		account := domain.Account{ID: accountID, Email: state.Email, IMAPHost: host, IMAPPort: port, IMAPUsername: state.IMAPUsername, MailboxType: state.MailboxType}
		if state.MailboxType != domain.MailboxTypeICloud || domain.UsesForwardedICloudIMAP(account) {
			return ErrICloudMailboxRequired
		}
	}
	if _, err := s.txExecContext(ctx, tx, `
INSERT INTO account_mail_transports(account_id, transport, updated_at) VALUES(?, ?, ?)
ON CONFLICT(account_id) DO UPDATE SET transport = excluded.transport, updated_at = excluded.updated_at`, accountID, transport, timestamp(s.now())); err != nil {
		return err
	}
	return tx.Commit()
}
