package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"icloud-api/internal/domain"
)

const (
	appleAccountCreationChannel = "apple_account"
	icloudWebCreationChannel    = "icloud_web"
)

func (s *Store) migrateAppleCreationChannelBudget(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS apple_creation_channel_attempts (
			account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			channel TEXT NOT NULL CHECK(channel IN ('apple_account','icloud_web')), attempted_at BIGINT NOT NULL,
			PRIMARY KEY (account_id, channel, attempted_at))`,
		`CREATE TABLE IF NOT EXISTS apple_creation_channel_cooldowns (
			account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			channel TEXT NOT NULL CHECK(channel IN ('apple_account','icloud_web')), until_at BIGINT NOT NULL,
			PRIMARY KEY (account_id, channel))`,
	} {
		if _, err := s.txExecContext(ctx, tx, statement); err != nil {
			return fmt.Errorf("migrate Apple creation channel budget: %w", err)
		}
	}
	marker, err := s.txExecContext(ctx, tx, `INSERT INTO apple_creation_budget_migrations(version, applied_at)
		VALUES (2, ?) ON CONFLICT(version) DO NOTHING`, timestamp(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("mark Apple creation channel migration: %w", err)
	}
	inserted, err := marker.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		return nil
	}
	cutoff := timestamp(time.Now().UTC().Add(-time.Hour))
	for _, channel := range []string{appleAccountCreationChannel, icloudWebCreationChannel} {
		if _, err := s.txExecContext(ctx, tx, `INSERT INTO apple_creation_channel_attempts(account_id, channel, attempted_at)
			SELECT account_id, ?, attempted_at FROM apple_creation_attempts WHERE attempted_at > ?
			ON CONFLICT (account_id, channel, attempted_at) DO NOTHING`, channel, cutoff); err != nil {
			return fmt.Errorf("seed Apple creation channel budget: %w", err)
		}
	}
	return nil
}

func appleCreationChannelLimit(channel string) (int, bool) {
	switch channel {
	case appleAccountCreationChannel:
		return domain.AppleAccountCreationHourlyLimit, true
	case icloudWebCreationChannel:
		return domain.ICloudWebCreationHourlyLimit, true
	default:
		return 0, false
	}
}

// ClaimAppleChannelCreationAttempt consumes one channel's rolling hourly budget.
func (s *Store) ClaimAppleChannelCreationAttempt(ctx context.Context, accountID int64, channel string, now time.Time) error {
	limit, ok := appleCreationChannelLimit(channel)
	if accountID < 1 || now.IsZero() || !ok {
		return errors.New("claim Apple channel creation attempt: invalid account or channel")
	}
	now = now.UTC()
	tx, err := s.beginAddressNamespaceTx(ctx)
	if err != nil {
		return fmt.Errorf("begin Apple channel budget claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.lockAccountVersionForUpdate(ctx, tx, accountID); err != nil {
		return fmt.Errorf("lock account for Apple channel budget: %w", err)
	}
	var enabled bool
	if err := s.txQueryRowContext(ctx, tx, `SELECT enabled FROM accounts WHERE id = ?`, accountID).Scan(&enabled); err != nil {
		return fmt.Errorf("read account for Apple channel budget: %w", err)
	}
	if !enabled {
		return ErrAccountDisabled
	}
	var cooldown, oldest sql.NullInt64
	err = s.txQueryRowContext(ctx, tx, `SELECT until_at FROM apple_creation_channel_cooldowns WHERE account_id = ? AND channel = ?`, accountID, channel).Scan(&cooldown)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("read Apple channel cooldown: %w", err)
	}
	var count int
	if err := s.txQueryRowContext(ctx, tx, `SELECT COUNT(*), MIN(attempted_at) FROM (SELECT attempted_at FROM apple_creation_channel_attempts WHERE account_id = ? AND channel = ? AND attempted_at > ? ORDER BY attempted_at DESC LIMIT ?) q`, accountID, channel, timestamp(now.Add(-time.Hour)), limit).Scan(&count, &oldest); err != nil {
		return fmt.Errorf("count Apple channel attempts: %w", err)
	}
	var wait time.Time
	if cooldown.Valid {
		wait = timeFromTimestamp(cooldown.Int64)
	}
	if count >= limit && oldest.Valid {
		wait = laterTime(wait, timeFromTimestamp(oldest.Int64).Add(time.Hour))
	}
	if wait.After(now) {
		if err := tx.Commit(); err != nil {
			return err
		}
		return &AppleCreationBudgetError{Until: wait, Now: now}
	}
	if _, err := s.txExecContext(ctx, tx, `DELETE FROM apple_creation_channel_attempts WHERE account_id = ? AND channel = ? AND attempted_at <= ?`, accountID, channel, timestamp(now.Add(-time.Hour))); err != nil {
		return err
	}
	if _, err := s.txExecContext(ctx, tx, `INSERT INTO apple_creation_channel_attempts(account_id, channel, attempted_at) VALUES (?, ?, ?)`, accountID, channel, timestamp(now)); err != nil {
		return err
	}
	return tx.Commit()
}

// PauseAppleChannelCreation stores the maximum channel cooldown deadline.
func (s *Store) PauseAppleChannelCreation(ctx context.Context, accountID int64, channel string, until time.Time) error {
	if accountID < 1 || until.IsZero() {
		return errors.New("pause Apple channel creation: account ID and deadline are required")
	}
	if _, ok := appleCreationChannelLimit(channel); !ok {
		return errors.New("pause Apple channel creation: invalid channel")
	}
	tx, err := s.beginAddressNamespaceTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.lockAccountVersionForUpdate(ctx, tx, accountID); err != nil {
		return err
	}
	if _, err := s.txExecContext(ctx, tx, `INSERT INTO apple_creation_channel_cooldowns(account_id, channel, until_at) VALUES (?, ?, ?) ON CONFLICT (account_id, channel) DO UPDATE SET until_at = CASE WHEN excluded.until_at > apple_creation_channel_cooldowns.until_at THEN excluded.until_at ELSE apple_creation_channel_cooldowns.until_at END`, accountID, channel, timestamp(until.UTC())); err != nil {
		return err
	}
	return tx.Commit()
}
