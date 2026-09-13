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
	AppleCreationHourlyLimit = domain.AppleCreationHourlyLimit
	AppleCreationDailyLimit  = domain.AppleCreationDailyLimit
	AppleCreationMinInterval = domain.AppleCreationMinInterval

	appleCreationHourWindow = time.Hour
	appleCreationDayWindow  = 24 * time.Hour
)

const appleCreationBudgetDiagnosticCode = "APPLE_CREATION_BUDGET_WAIT"

// AppleCreationBudgetError reports the earliest time at which a shared Apple
// alias-creation budget may be claimed again.
type AppleCreationBudgetError struct {
	Until time.Time
	Now   time.Time
}

func (e *AppleCreationBudgetError) Error() string { return appleCreationBudgetDiagnosticCode }

func (e *AppleCreationBudgetError) DiagnosticCode() string {
	return appleCreationBudgetDiagnosticCode
}

func (e *AppleCreationBudgetError) RetryDelay() time.Duration {
	if e == nil {
		return time.Second
	}
	delay := e.Until.Sub(e.Now)
	if delay < time.Second {
		return time.Second
	}
	return delay
}

func (s *Store) migrateAppleCreationBudget(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS apple_creation_probe_attempts (
			account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			attempted_at BIGINT NOT NULL,
			PRIMARY KEY (account_id, attempted_at))`,
		`CREATE TABLE IF NOT EXISTS apple_creation_attempts (
			account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
			attempted_at BIGINT NOT NULL,
			PRIMARY KEY (account_id, attempted_at))`,
		`CREATE TABLE IF NOT EXISTS apple_creation_cooldowns (
			account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
			until_at BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS apple_creation_budget_migrations (
			version INTEGER PRIMARY KEY, applied_at BIGINT NOT NULL)`,
	} {
		if _, err := s.txExecContext(ctx, tx, statement); err != nil {
			return fmt.Errorf("migrate Apple creation budget: %w", err)
		}
	}
	// Seed once: later successes already have an attempt recorded at a different
	// timestamp, so reseeding after a restart would double-charge that activity.
	marker, err := s.txExecContext(ctx, tx, `INSERT INTO apple_creation_budget_migrations(version, applied_at)
		VALUES (1, ?) ON CONFLICT(version) DO NOTHING`, timestamp(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("mark Apple creation budget migration: %w", err)
	}
	inserted, err := marker.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		return nil
	}
	_, err = s.txExecContext(ctx, tx, `
		INSERT INTO apple_creation_attempts(account_id, attempted_at)
		SELECT DISTINCT account_id, created_at
		FROM apple_alias_creation_events
		WHERE created_at > ?
		ON CONFLICT (account_id, attempted_at) DO NOTHING`, timestamp(time.Now().UTC().Add(-appleCreationDayWindow)))
	if err != nil {
		return fmt.Errorf("seed Apple creation budget: %w", err)
	}
	return nil
}

// ClaimAppleCreationAttempt atomically checks and consumes one account's
// shared Apple alias-creation budget. Failed upstream attempts remain charged.
func (s *Store) ClaimAppleCreationAttempt(ctx context.Context, accountID int64, now time.Time) error {
	return s.claimAppleCreationAttempt(ctx, accountID, now, false)
}

// Manual probes have their own attempt ledger and do not consume, clear or
// extend the background creation budget and Apple cooldown.
func (s *Store) ClaimAppleCreationProbe(ctx context.Context, accountID int64, now time.Time) error {
	return s.claimAppleCreationAttempt(ctx, accountID, now, true)
}

func (s *Store) claimAppleCreationAttempt(ctx context.Context, accountID int64, now time.Time, probe bool) error {
	if accountID < 1 || now.IsZero() {
		return errors.New("claim Apple creation attempt: account ID and time are required")
	}
	now = now.UTC()
	tx, err := s.beginAddressNamespaceTx(ctx)
	if err != nil {
		return fmt.Errorf("begin Apple creation budget claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.lockAccountVersionForUpdate(ctx, tx, accountID); err != nil {
		return fmt.Errorf("lock account for Apple creation budget: %w", err)
	}
	var enabled bool
	if err := s.txQueryRowContext(ctx, tx, `SELECT enabled FROM accounts WHERE id = ?`, accountID).Scan(&enabled); err != nil {
		return fmt.Errorf("read account for Apple creation budget: %w", err)
	}
	if !enabled {
		return ErrAccountDisabled
	}

	// Table names are fixed internal identifiers, never request input.
	attemptTable := "apple_creation_attempts"
	var cooldownUntil int64
	if probe {
		attemptTable = "apple_creation_probe_attempts"
	} else {
		err = s.txQueryRowContext(ctx, tx,
			`SELECT until_at FROM apple_creation_cooldowns WHERE account_id = ?`, accountID,
		).Scan(&cooldownUntil)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("read Apple creation cooldown: %w", err)
		}
	}
	var hourlyCount, dailyCount int
	var oldestHourly, oldestDaily, latestAttempt sql.NullInt64
	if err := s.txQueryRowContext(ctx, tx, `
		SELECT COUNT(*), MIN(attempted_at), MAX(attempted_at)
		FROM (SELECT attempted_at FROM `+attemptTable+`
		WHERE account_id = ? AND attempted_at > ?
		ORDER BY attempted_at DESC LIMIT ?) budget_window`,
		accountID, timestamp(now.Add(-appleCreationHourWindow)), AppleCreationHourlyLimit,
	).Scan(&hourlyCount, &oldestHourly, &latestAttempt); err != nil {
		return fmt.Errorf("count hourly Apple creation attempts: %w", err)
	}
	if err := s.txQueryRowContext(ctx, tx, `
		SELECT COUNT(*), MIN(attempted_at)
		FROM (SELECT attempted_at FROM `+attemptTable+`
		WHERE account_id = ? AND attempted_at > ?
		ORDER BY attempted_at DESC LIMIT ?) budget_window`,
		accountID, timestamp(now.Add(-appleCreationDayWindow)), AppleCreationDailyLimit,
	).Scan(&dailyCount, &oldestDaily); err != nil {
		return fmt.Errorf("count daily Apple creation attempts: %w", err)
	}

	var waitUntil time.Time
	if cooldownUntil > timestamp(now) {
		waitUntil = timeFromTimestamp(cooldownUntil)
	}
	if hourlyCount >= AppleCreationHourlyLimit && oldestHourly.Valid {
		waitUntil = laterTime(waitUntil, timeFromTimestamp(oldestHourly.Int64).Add(appleCreationHourWindow))
	}
	if dailyCount >= AppleCreationDailyLimit && oldestDaily.Valid {
		waitUntil = laterTime(waitUntil, timeFromTimestamp(oldestDaily.Int64).Add(appleCreationDayWindow))
	}
	if latestAttempt.Valid {
		waitUntil = laterTime(waitUntil, timeFromTimestamp(latestAttempt.Int64).Add(AppleCreationMinInterval))
	}
	if waitUntil.After(now) {
		// Commit the lock-only transaction: rejected attempts must not consume
		// quota or erase an existing cooldown.
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit rejected Apple creation budget claim: %w", err)
		}
		return &AppleCreationBudgetError{Until: waitUntil, Now: now}
	}

	if _, err := s.txExecContext(ctx, tx,
		`DELETE FROM `+attemptTable+` WHERE account_id = ? AND attempted_at <= ?`,
		accountID, timestamp(now.Add(-appleCreationDayWindow)),
	); err != nil {
		return fmt.Errorf("expire Apple creation attempts: %w", err)
	}
	if _, err := s.txExecContext(ctx, tx, `
		INSERT INTO `+attemptTable+`(account_id, attempted_at) VALUES (?, ?)`,
		accountID, timestamp(now)); err != nil {
		return fmt.Errorf("record Apple creation attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Apple creation budget claim: %w", err)
	}
	return nil
}

// PauseAppleCreation stores the maximum cooldown deadline for an account.
func (s *Store) PauseAppleCreation(ctx context.Context, accountID int64, until time.Time) error {
	if accountID < 1 || until.IsZero() {
		return errors.New("pause Apple creation: account ID and deadline are required")
	}
	tx, err := s.beginAddressNamespaceTx(ctx)
	if err != nil {
		return fmt.Errorf("begin Apple creation pause: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.lockAccountVersionForUpdate(ctx, tx, accountID); err != nil {
		return fmt.Errorf("lock account for Apple creation pause: %w", err)
	}
	if _, err := s.txExecContext(ctx, tx, `
		INSERT INTO apple_creation_cooldowns(account_id, until_at) VALUES (?, ?)
		ON CONFLICT (account_id) DO UPDATE SET until_at =
		CASE WHEN excluded.until_at > apple_creation_cooldowns.until_at
		THEN excluded.until_at ELSE apple_creation_cooldowns.until_at END`,
		accountID, timestamp(until)); err != nil {
		return fmt.Errorf("store Apple creation pause: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Apple creation pause: %w", err)
	}
	return nil
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
