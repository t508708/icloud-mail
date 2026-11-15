package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"icloud-api/internal/domain"
)

func (s *Store) poolAliasRetirementReconcileOwnerTx(ctx context.Context, tx *sql.Tx, job PoolAliasRetirementJob, clientID string) error {
	if job.ClientID != clientID {
		return ErrNotFound
	}
	var project string
	var enabled bool
	if err := s.txQueryRowContext(ctx, tx, `SELECT project,enabled FROM pool_clients WHERE id=?`, clientID).Scan(&project, &enabled); err != nil {
		return err
	}
	if !enabled {
		return ErrPoolClosed
	}
	if project != job.Project {
		return ErrPoolRetirementProject
	}
	return nil
}

// Recheck the durable ownership snapshot under the Pool lock. An absent alias
// is accepted only after its foreign key has become NULL, never after rebinding.
func (s *Store) poolAliasRetirementReconcileBindingTx(ctx context.Context, tx *sql.Tx, job PoolAliasRetirementJob, item PoolAliasRetirementJobItem) (PoolLease, domain.Alias, bool, error) {
	lease, err := scanPoolLease(s.txQueryRowContext(ctx, tx, poolLeaseSelect+` WHERE l.id=?`, item.LeaseID))
	if err != nil {
		return lease, domain.Alias{}, false, ErrPoolRetirementConflict
	}
	if lease.ClientID != job.ClientID || lease.Project != job.Project || domain.NormalizeEmail(lease.Address) != domain.NormalizeEmail(item.Address) {
		return lease, domain.Alias{}, false, ErrPoolRetirementConflict
	}
	if item.State == "retired" {
		if lease.State != "retired" || lease.AliasID != 0 {
			return lease, domain.Alias{}, false, ErrPoolRetirementConflict
		}
		return lease, domain.Alias{}, false, nil
	}
	if lease.State != "retiring" && lease.State != "used" {
		return lease, domain.Alias{}, false, ErrPoolRetirementConflict
	}
	if lease.AliasID != 0 && lease.AliasID != item.AliasID {
		return lease, domain.Alias{}, false, ErrPoolRetirementConflict
	}
	alias, err := s.getAliasByIDTx(ctx, tx, item.AliasID)
	if errors.Is(err, ErrNotFound) {
		if lease.AliasID != 0 {
			return lease, domain.Alias{}, false, ErrPoolRetirementConflict
		}
		return lease, domain.Alias{}, false, nil
	}
	if err != nil {
		return lease, domain.Alias{}, false, err
	}
	if lease.AliasID != item.AliasID || alias.AccountID != item.AccountID || domain.NormalizeEmail(alias.Address) != domain.NormalizeEmail(item.Address) {
		return lease, domain.Alias{}, false, ErrPoolRetirementConflict
	}
	return lease, alias, true, nil
}

// AcquirePoolAliasRetirementReconcile reserves the original job without
// changing unresolved items or reopening their leases. Apple reads run later.
func (s *Store) AcquirePoolAliasRetirementReconcile(ctx context.Context, clientID, operationID string, items []PoolAliasRetirementItemInput) (PoolAliasRetirementJob, bool, error) {
	tx, err := s.poolTx(ctx)
	if err != nil {
		return PoolAliasRetirementJob{}, false, err
	}
	defer tx.Rollback()
	job, err := s.poolAliasRetirementJobTx(ctx, tx, operationID)
	if err != nil {
		return job, false, err
	}
	if err := s.poolAliasRetirementReconcileOwnerTx(ctx, tx, job, clientID); err != nil {
		return PoolAliasRetirementJob{}, false, err
	}
	if len(items) != len(job.Items) {
		return PoolAliasRetirementJob{}, false, ErrPoolRequestConflict
	}
	allRetired := true
	for index, item := range job.Items {
		if item.AliasID != items[index].AliasID || item.LeaseID != strings.TrimSpace(items[index].LeaseID) {
			return PoolAliasRetirementJob{}, false, ErrPoolRequestConflict
		}
		allRetired = allRetired && item.State == "retired"
	}
	if job.Status == PoolAliasRetirementJobQueued || job.Status == PoolAliasRetirementJobRunning || job.Status == PoolAliasRetirementJobCompleted && allRetired {
		return job, false, tx.Commit()
	}
	if job.Status != PoolAliasRetirementJobReview && job.Status != PoolAliasRetirementJobInterrupted && job.Status != PoolAliasRetirementJobCompleted {
		return PoolAliasRetirementJob{}, false, ErrPoolRetirementConflict
	}
	for _, item := range job.Items {
		if item.State != "review" && item.State != "retiring" && item.State != "used" && item.State != "retired" {
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementConflict
		}
		if item.State != "retired" {
			if err := s.poolAliasRetirementOtherOwnerTx(ctx, tx, operationID, item.LeaseID); err != nil {
				return PoolAliasRetirementJob{}, false, err
			}
		}
		if _, _, _, err := s.poolAliasRetirementReconcileBindingTx(ctx, tx, job, item); err != nil {
			return PoolAliasRetirementJob{}, false, err
		}
	}
	now := s.now().UTC()
	result, err := s.txExecContext(ctx, tx, `UPDATE pool_alias_retirement_jobs SET status='running',updated_at=? WHERE operation_id=? AND client_id=? AND status=?`, timestamp(now), operationID, clientID, job.Status)
	if err != nil {
		return PoolAliasRetirementJob{}, false, err
	}
	if err := requireAffected(result, "reconciled retirement job"); err != nil {
		return PoolAliasRetirementJob{}, false, ErrPoolRetirementConflict
	}
	if err := tx.Commit(); err != nil {
		return PoolAliasRetirementJob{}, false, err
	}
	job.Status, job.UpdatedAt = PoolAliasRetirementJobRunning, now
	return job, true, nil
}

func (s *Store) poolAliasRetirementOtherOwnerTx(ctx context.Context, tx *sql.Tx, operationID, leaseID string) error {
	var count int
	// Explicitly failed, completed attempts are history. An unresolved attempt
	// or a newer live executor owns the lease until it publishes its outcome.
	err := s.txQueryRowContext(ctx, tx, `SELECT COUNT(*) FROM pool_alias_retirement_job_items i
		JOIN pool_alias_retirement_jobs j ON j.operation_id=i.operation_id
		WHERE i.lease_id=? AND i.operation_id<>? AND i.state<>'retired'
		AND (i.state IN ('retiring','review') OR j.status<>'completed')`, leaseID, operationID).Scan(&count)
	if err != nil {
		return err
	}
	if count != 0 {
		return ErrPoolRetirementConflict
	}
	return nil
}

// PreparePoolAliasRetirementResume is an explicit opt-in before remote writes.
// Merely acquiring or completing reconciliation leaves unknown leases closed.
func (s *Store) PreparePoolAliasRetirementResume(ctx context.Context, clientID, operationID string, ordinal int) error {
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, item, err := s.poolAliasRetirementItemTx(ctx, tx, operationID, ordinal)
	if err != nil {
		return err
	}
	if err := s.poolAliasRetirementReconcileOwnerTx(ctx, tx, job, clientID); err != nil {
		return err
	}
	if job.Status != PoolAliasRetirementJobRunning || item.State != "review" && item.State != "used" && item.State != "retiring" {
		return ErrPoolRetirementConflict
	}
	if err := s.poolAliasRetirementOtherOwnerTx(ctx, tx, operationID, item.LeaseID); err != nil {
		return err
	}
	if _, err := s.lockAccountVersionForUpdate(ctx, tx, item.AccountID); err != nil {
		return err
	}
	lease, _, exists, err := s.poolAliasRetirementReconcileBindingTx(ctx, tx, job, item)
	if err != nil {
		return err
	}
	if !exists {
		return ErrPoolRetirementConflict
	}
	shared, err := s.poolAliasHasOtherProjectUsersTx(ctx, tx, item.AliasID, clientID)
	if err != nil {
		return err
	}
	if shared {
		return ErrPoolRetirementShared
	}
	var accountEnabled bool
	if err := s.txQueryRowContext(ctx, tx, `SELECT enabled FROM accounts WHERE id=?`, item.AccountID).Scan(&accountEnabled); err != nil {
		return err
	}
	now := timestamp(s.now())
	result, err := s.txExecContext(ctx, tx, `UPDATE pool_leases SET state='retiring',updated_at=? WHERE id=? AND client_id=? AND alias_id=? AND state=?`, now, item.LeaseID, clientID, item.AliasID, lease.State)
	if err != nil {
		return err
	}
	if err := requireAffected(result, "resumed retirement lease"); err != nil {
		return ErrPoolRetirementConflict
	}
	memberTable := "pool_members"
	if !accountEnabled {
		memberTable = "pool_suspended_members"
	}
	result, err = s.txExecContext(ctx, tx, `UPDATE `+memberTable+` SET state='paused',updated_at=? WHERE alias_id=? AND state IN ('used','paused')`, now, item.AliasID)
	if err != nil {
		return err
	}
	if err := requireAffected(result, "resumed retirement member"); err != nil {
		return ErrPoolRetirementConflict
	}
	if _, err := s.txExecContext(ctx, tx, `UPDATE pool_alias_retirement_job_items SET state='retiring',result_code='',result_message='',updated_at=? WHERE operation_id=? AND ordinal=?`, now, operationID, ordinal); err != nil {
		return err
	}
	return tx.Commit()
}

// FinalizeReconciledPoolAliasRetirement publishes freshly confirmed Apple
// absence. The caller supplies that evidence; no network runs in this transaction.
func (s *Store) FinalizeReconciledPoolAliasRetirement(ctx context.Context, clientID, operationID string, ordinal int) error {
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, item, err := s.poolAliasRetirementItemTx(ctx, tx, operationID, ordinal)
	if err != nil {
		return err
	}
	if err := s.poolAliasRetirementReconcileOwnerTx(ctx, tx, job, clientID); err != nil {
		return err
	}
	if item.State != "review" && item.State != "retiring" && item.State != "used" && item.State != "retired" {
		return ErrPoolRetirementConflict
	}
	if err := s.poolAliasRetirementOtherOwnerTx(ctx, tx, operationID, item.LeaseID); err != nil {
		return err
	}
	lease, alias, exists, err := s.poolAliasRetirementReconcileBindingTx(ctx, tx, job, item)
	if err != nil {
		return err
	}
	if item.State == "retired" {
		return tx.Commit()
	}
	if item.State == "used" && lease.State != "used" || item.State != "used" && lease.State != "retiring" {
		return ErrPoolRetirementConflict
	}
	if exists {
		version, err := s.lockAccountVersionForUpdate(ctx, tx, item.AccountID)
		if err != nil {
			return err
		}
		_, alias, exists, err = s.poolAliasRetirementReconcileBindingTx(ctx, tx, job, item)
		if err != nil || !exists {
			return ErrPoolRetirementConflict
		}
		shared, err := s.poolAliasHasOtherProjectUsersTx(ctx, tx, item.AliasID, clientID)
		if err != nil {
			return err
		}
		if shared {
			return ErrPoolRetirementShared
		}
		result, err := s.txExecContext(ctx, tx, `DELETE FROM aliases WHERE id=? AND account_id=?`, item.AliasID, item.AccountID)
		if err != nil {
			return err
		}
		if err := requireAffected(result, "reconciled retirement alias"); err != nil {
			return ErrPoolRetirementConflict
		}
		if alias.Enabled {
			if _, err := s.bumpAccountVersionTx(ctx, tx, item.AccountID, version); err != nil {
				return err
			}
		}
	}
	now := timestamp(s.now())
	result, err := s.txExecContext(ctx, tx, `UPDATE pool_leases SET alias_id=NULL,state='retired',updated_at=? WHERE id=? AND client_id=? AND state=?`, now, item.LeaseID, clientID, lease.State)
	if err != nil {
		return err
	}
	if err := requireAffected(result, "reconciled retirement lease"); err != nil {
		return ErrPoolRetirementConflict
	}
	if _, err := s.txExecContext(ctx, tx, `UPDATE pool_alias_retirement_job_items SET state='retired',result_code='',result_message='',updated_at=? WHERE operation_id=? AND ordinal=?`, now, operationID, ordinal); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordPoolAliasRetirementReconcileReview records read uncertainty without
// releasing the running executor reservation or reopening an unresolved lease.
func (s *Store) RecordPoolAliasRetirementReconcileReview(ctx context.Context, clientID, operationID string, ordinal int, code, message string) error {
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, item, err := s.poolAliasRetirementItemTx(ctx, tx, operationID, ordinal)
	if err != nil {
		return err
	}
	if err := s.poolAliasRetirementReconcileOwnerTx(ctx, tx, job, clientID); err != nil {
		return err
	}
	if job.Status != PoolAliasRetirementJobRunning {
		return ErrPoolRetirementConflict
	}
	lease, _, exists, err := s.poolAliasRetirementReconcileBindingTx(ctx, tx, job, item)
	if err != nil {
		return err
	}
	if exists {
		if _, err := s.lockAccountVersionForUpdate(ctx, tx, item.AccountID); err != nil {
			return err
		}
		lease, _, _, err = s.poolAliasRetirementReconcileBindingTx(ctx, tx, job, item)
		if err != nil {
			return err
		}
	}
	// A fresh read does not invalidate an earlier definitive failure. Only
	// PreparePoolAliasRetirementResume may opt a used item back into mutation.
	if item.State == "retired" || item.State == "used" {
		return tx.Commit()
	}
	if item.State != "review" && item.State != "retiring" || lease.State != "retiring" {
		return ErrPoolRetirementConflict
	}
	result, err := s.txExecContext(ctx, tx, `UPDATE pool_alias_retirement_job_items SET state='review',result_code=?,result_message=?,updated_at=?
		WHERE operation_id=? AND ordinal=? AND state=?`, strings.TrimSpace(code), strings.TrimSpace(message), timestamp(s.now()), operationID, ordinal, item.State)
	if err != nil {
		return err
	}
	if err := requireAffected(result, "reconciled review item"); err != nil {
		return ErrPoolRetirementConflict
	}
	return tx.Commit()
}

func (s *Store) CompletePoolAliasRetirementReconcile(ctx context.Context, clientID, operationID string) error {
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, err := s.poolAliasRetirementJobTx(ctx, tx, operationID)
	if err != nil {
		return err
	}
	if err := s.poolAliasRetirementReconcileOwnerTx(ctx, tx, job, clientID); err != nil {
		return err
	}
	status := PoolAliasRetirementJobCompleted
	for _, item := range job.Items {
		if item.State == "retiring" || item.State == "review" {
			status = PoolAliasRetirementJobReview
		}
	}
	if job.Status == status {
		return tx.Commit()
	}
	if job.Status != PoolAliasRetirementJobRunning {
		return ErrPoolRetirementConflict
	}
	result, err := s.txExecContext(ctx, tx, `UPDATE pool_alias_retirement_jobs SET status=?,updated_at=? WHERE operation_id=? AND client_id=? AND status='running'`, status, timestamp(s.now()), operationID, clientID)
	if err != nil {
		return err
	}
	if err := requireAffected(result, "completed reconciliation job"); err != nil {
		return ErrPoolRetirementConflict
	}
	return tx.Commit()
}
