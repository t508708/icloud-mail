package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"icloud-api/internal/domain"
)

// UpgradeAliasCreationCadence replaces opted-in plans once at startup. It
// preserves attempt history, opt-outs, and a future error/cooldown deadline.
func (s *Store) UpgradeAliasCreationCadence(ctx context.Context, version string, now time.Time, plan func(time.Time) ([]time.Time, error)) error {
	if version == "" || plan == nil {
		return errors.New("cadence version and planner required")
	}
	if _, err := s.execContext(ctx, `CREATE TABLE IF NOT EXISTS alias_creation_cadence_versions(version TEXT PRIMARY KEY,applied_at BIGINT NOT NULL)`); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := s.txExecContext(ctx, tx, `INSERT INTO alias_creation_cadence_versions(version,applied_at) VALUES(?,?) ON CONFLICT(version) DO NOTHING`, version, timestamp(now))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return tx.Commit()
	}
	rows, err := s.txQueryContext(ctx, tx, `SELECT account_id,enabled,planned_at_json,next_run_at,last_attempted_at,last_created_at,last_alias_address,last_error,created_at,updated_at FROM alias_creation_schedules WHERE enabled=TRUE`)
	if err != nil {
		return err
	}
	var schedules []domain.AliasCreationSchedule
	for rows.Next() {
		schedule, e := scanAliasCreationSchedule(rows)
		if e != nil {
			rows.Close()
			return e
		}
		schedules = append(schedules, schedule)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, schedule := range schedules {
		anchor := now.UTC()
		if schedule.LastAttemptedAt != nil && schedule.LastAttemptedAt.After(anchor) {
			anchor = *schedule.LastAttemptedAt
		}
		// A failed previous attempt can carry a server Retry-After. Do not
		// erase that deadline merely because local frequency has increased.
		localBudgetWait := strings.Contains(schedule.LastError, "APPLE_CREATION_BUDGET_WAIT") ||
			strings.Contains(schedule.LastError, "本地主号创建预算已用尽")
		if schedule.LastError != "" && !localBudgetWait && schedule.NextRunAt != nil && schedule.NextRunAt.After(anchor) {
			anchor = *schedule.NextRunAt
		}
		planned, err := plan(anchor)
		if err != nil {
			return err
		}
		if len(planned) == 0 || !planned[0].After(anchor) {
			return errors.New("cadence planner returned invalid deadlines")
		}
		encoded, err := encodePlannedAliasCreationTimes(planned)
		if err != nil {
			return err
		}
		result, err := s.txExecContext(ctx, tx, `UPDATE alias_creation_schedules SET planned_at_json=?,next_run_at=?,updated_at=? WHERE account_id=? AND enabled=TRUE`, encoded, timestamp(planned[0]), timestamp(now), schedule.AccountID)
		if err != nil {
			return err
		}
		if err := requireAffected(result, "cadence schedule"); err != nil {
			return fmt.Errorf("upgrade cadence: %w", err)
		}
	}
	return tx.Commit()
}
