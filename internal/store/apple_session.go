package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"icloud-api/internal/domain"
)

const appleWebSessionColumns = `account_id, session_ciphertext, apple_id, region, authenticated, last_validated_at, created_at, updated_at`
const appleWebSessionTable = "apple_web_sessions"
const appleAccountSessionTable = "apple_account_sessions"

// The two channels share metadata shape, but their persisted sessions are independent.
func (s *Store) upsertAppleSession(ctx context.Context, v domain.AppleWebSession, table string) (domain.AppleWebSession, error) {
	if v.AccountID < 1 {
		return domain.AppleWebSession{}, fmt.Errorf("upsert apple session: account id must be positive")
	}
	if strings.TrimSpace(v.Ciphertext) == "" {
		return domain.AppleWebSession{}, fmt.Errorf("upsert apple session: ciphertext is empty")
	}
	appleID := strings.TrimSpace(sanitizePostgresText(v.AppleID))
	if appleID == "" {
		return domain.AppleWebSession{}, fmt.Errorf("upsert apple session: apple id is empty")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.AppleWebSession{}, fmt.Errorf("begin apple session upsert: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err = s.lockAccountVersionForUpdate(ctx, tx, v.AccountID); err != nil {
		return domain.AppleWebSession{}, fmt.Errorf("lock account for apple session upsert: %w", err)
	}
	state, err := s.readAccountMailboxStateTx(ctx, tx, v.AccountID)
	if err != nil {
		return domain.AppleWebSession{}, fmt.Errorf("read account for apple session upsert: %w", err)
	}
	if state.MailboxType != domain.MailboxTypeICloud {
		return domain.AppleWebSession{}, ErrICloudMailboxRequired
	}
	now := s.now()
	q := fmt.Sprintf(`INSERT INTO %s(
		account_id, session_ciphertext, apple_id, region, authenticated,
		last_validated_at, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(account_id) DO UPDATE SET
		session_ciphertext = excluded.session_ciphertext,
		apple_id = excluded.apple_id,
		region = excluded.region,
		authenticated = excluded.authenticated,
		last_validated_at = excluded.last_validated_at,
		updated_at = excluded.updated_at`, table)
	if _, err = s.txExecContext(ctx, tx, q, v.AccountID, v.Ciphertext, appleID, strings.TrimSpace(sanitizePostgresText(v.Region)), v.Authenticated, nullableTimestamp(v.LastValidatedAt), timestamp(now), timestamp(now)); err != nil {
		return domain.AppleWebSession{}, fmt.Errorf("upsert apple session: %w", err)
	}
	saved, err := scanAppleWebSession(s.txQueryRowContext(ctx, tx, fmt.Sprintf("SELECT %s FROM %s WHERE account_id = ?", appleWebSessionColumns, table), v.AccountID))
	if err != nil {
		return domain.AppleWebSession{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.AppleWebSession{}, fmt.Errorf("commit apple session upsert: %w", err)
	}
	return saved, nil
}
func (s *Store) UpsertAppleWebSession(c context.Context, v domain.AppleWebSession) (domain.AppleWebSession, error) {
	return s.upsertAppleSession(c, v, appleWebSessionTable)
}
func (s *Store) UpsertAppleAccountSession(c context.Context, v domain.AppleWebSession) (domain.AppleWebSession, error) {
	return s.upsertAppleSession(c, v, appleAccountSessionTable)
}
func (s *Store) getAppleSession(c context.Context, id int64, t string) (domain.AppleWebSession, error) {
	return scanAppleWebSession(s.queryRowContext(c, fmt.Sprintf("SELECT %s FROM %s WHERE account_id = ?", appleWebSessionColumns, t), id))
}
func (s *Store) GetAppleWebSession(c context.Context, id int64) (domain.AppleWebSession, error) {
	return s.getAppleSession(c, id, appleWebSessionTable)
}
func (s *Store) GetAppleAccountSession(c context.Context, id int64) (domain.AppleWebSession, error) {
	return s.getAppleSession(c, id, appleAccountSessionTable)
}
func (s *Store) deleteAppleSession(c context.Context, id int64, t, l string) error {
	r, e := s.execContext(c, fmt.Sprintf("DELETE FROM %s WHERE account_id = ?", t), id)
	if e != nil {
		return fmt.Errorf("delete %s: %w", l, e)
	}
	return requireAffected(r, l)
}
func (s *Store) DeleteAppleWebSession(c context.Context, id int64) error {
	return s.deleteAppleSession(c, id, appleWebSessionTable, "apple web session")
}
func (s *Store) DeleteAppleAccountSession(c context.Context, id int64) error {
	return s.deleteAppleSession(c, id, appleAccountSessionTable, "apple account session")
}
func scanAppleWebSession(x rowScanner) (domain.AppleWebSession, error) {
	var v domain.AppleWebSession
	var a bool
	var lv sql.NullInt64
	var cr, up int64
	if e := x.Scan(&v.AccountID, &v.Ciphertext, &v.AppleID, &v.Region, &a, &lv, &cr, &up); e != nil {
		if e == sql.ErrNoRows {
			return v, ErrNotFound
		}
		return v, fmt.Errorf("scan apple web session: %w", e)
	}
	v.Authenticated = a
	v.LastValidatedAt = timePtr(lv)
	v.CreatedAt = timeFromTimestamp(cr)
	v.UpdatedAt = timeFromTimestamp(up)
	return v, nil
}
