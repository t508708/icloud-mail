package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"icloud-api/internal/domain"
)

const (
	PoolAliasRetirementJobQueued      = "queued"
	PoolAliasRetirementJobRunning     = "running"
	PoolAliasRetirementJobCompleted   = "completed"
	PoolAliasRetirementJobReview      = "review"
	PoolAliasRetirementJobInterrupted = "interrupted"
)

type PoolAliasRetirementItemInput struct {
	AliasID int64  `json:"alias_id"`
	LeaseID string `json:"lease_id"`
}

// PoolAliasRetirementJob deliberately contains only ownership and outcome
// metadata. It never carries mailbox credentials, session material, or mail.
type PoolAliasRetirementJob struct {
	OperationID string                       `json:"operation_id"`
	ClientID    string                       `json:"-"`
	Project     string                       `json:"project"`
	Status      string                       `json:"status"`
	RequestID   string                       `json:"request_id"`
	Items       []PoolAliasRetirementJobItem `json:"items"`
	CreatedAt   time.Time                    `json:"created_at"`
	UpdatedAt   time.Time                    `json:"updated_at"`
}

type PoolAliasRetirementJobItem struct {
	Ordinal       int    `json:"-"`
	AliasID       int64  `json:"alias_id"`
	LeaseID       string `json:"lease_id"`
	AccountID     int64  `json:"account_id"`
	Address       string `json:"address"`
	State         string `json:"state"`
	ResultCode    string `json:"result_code,omitempty"`
	ResultMessage string `json:"result_message,omitempty"`
}

func validPoolAliasRetirementOperationID(id string) bool {
	if len(id) < 16 || len(id) > 128 {
		return false
	}
	for _, char := range id {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_') {
			return false
		}
	}
	return true
}

func poolAliasRetirementFingerprint(items []PoolAliasRetirementItemInput) (string, error) {
	canonical := make([]PoolAliasRetirementItemInput, len(items))
	copy(canonical, items)
	for index := range canonical {
		canonical[index].LeaseID = strings.TrimSpace(canonical[index].LeaseID)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func validatePoolAliasRetirementInput(operationID string, items []PoolAliasRetirementItemInput) error {
	if !validPoolAliasRetirementOperationID(operationID) || len(items) == 0 || len(items) > 100 {
		return ErrPoolInput
	}
	aliases := make(map[int64]struct{}, len(items))
	leases := make(map[string]struct{}, len(items))
	for _, item := range items {
		item.LeaseID = strings.TrimSpace(item.LeaseID)
		if item.AliasID < 1 || item.LeaseID == "" || len(item.LeaseID) > 128 {
			return ErrPoolInput
		}
		if _, exists := aliases[item.AliasID]; exists {
			return ErrPoolInput
		}
		if _, exists := leases[item.LeaseID]; exists {
			return ErrPoolInput
		}
		aliases[item.AliasID] = struct{}{}
		leases[item.LeaseID] = struct{}{}
	}
	return nil
}

// ValidatePoolAliasRetirementInput lets HTTP boundaries reject malformed new
// work before checking Apple session state. Exact operation replay is checked
// first by callers so an existing key with changed contents still returns the
// stable idempotency conflict.
func ValidatePoolAliasRetirementInput(operationID string, items []PoolAliasRetirementItemInput) error {
	return validatePoolAliasRetirementInput(operationID, items)
}

func scanPoolAliasRetirementJob(row rowScanner) (PoolAliasRetirementJob, error) {
	var job PoolAliasRetirementJob
	var created, updated int64
	err := row.Scan(&job.OperationID, &job.ClientID, &job.Project, &job.Status, &job.RequestID, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return PoolAliasRetirementJob{}, ErrNotFound
	}
	if err != nil {
		return PoolAliasRetirementJob{}, err
	}
	job.CreatedAt = time.Unix(0, created).UTC()
	job.UpdatedAt = time.Unix(0, updated).UTC()
	job.Items = []PoolAliasRetirementJobItem{}
	return job, nil
}

func (s *Store) poolAliasRetirementJobTx(ctx context.Context, tx *sql.Tx, operationID string) (PoolAliasRetirementJob, error) {
	job, err := scanPoolAliasRetirementJob(s.txQueryRowContext(ctx, tx, `SELECT operation_id,client_id,project,status,request_id,created_at,updated_at
		FROM pool_alias_retirement_jobs WHERE operation_id=?`, operationID))
	if err != nil {
		return job, err
	}
	rows, err := s.txQueryContext(ctx, tx, `SELECT ordinal,alias_id,lease_id,account_id,address,state,result_code,result_message
		FROM pool_alias_retirement_job_items WHERE operation_id=? ORDER BY ordinal`, operationID)
	if err != nil {
		return PoolAliasRetirementJob{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item PoolAliasRetirementJobItem
		if err := rows.Scan(&item.Ordinal, &item.AliasID, &item.LeaseID, &item.AccountID, &item.Address, &item.State, &item.ResultCode, &item.ResultMessage); err != nil {
			return PoolAliasRetirementJob{}, err
		}
		job.Items = append(job.Items, item)
	}
	return job, rows.Err()
}

func (s *Store) GetPoolAliasRetirementJob(ctx context.Context, operationID, clientID string) (PoolAliasRetirementJob, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return PoolAliasRetirementJob{}, err
	}
	defer tx.Rollback()
	job, err := s.poolAliasRetirementJobTx(ctx, tx, operationID)
	if err != nil {
		return PoolAliasRetirementJob{}, err
	}
	if job.ClientID != clientID {
		return PoolAliasRetirementJob{}, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return PoolAliasRetirementJob{}, err
	}
	return job, nil
}

// PoolAliasRetirementAccounts is a read-only first pass used to validate that
// every target account still has a usable Apple session before changing leases.
// StartPoolAliasRetirement repeats all storage checks atomically.
func (s *Store) PoolAliasRetirementAccounts(ctx context.Context, clientID string, items []PoolAliasRetirementItemInput) ([]int64, error) {
	if len(items) == 0 {
		return nil, ErrPoolInput
	}
	accounts := make([]int64, 0, len(items))
	seen := make(map[int64]struct{}, len(items))
	for _, item := range items {
		lease, err := s.GetPoolLease(ctx, strings.TrimSpace(item.LeaseID), "")
		if errors.Is(err, ErrNotFound) {
			return nil, ErrPoolRetirementLeaseNotFound
		}
		if err != nil {
			return nil, err
		}
		if lease.ClientID != clientID {
			return nil, ErrPoolRetirementProject
		}
		if lease.AliasID != item.AliasID {
			return nil, ErrPoolRetirementAlias
		}
		if lease.State != "used" {
			return nil, ErrPoolRetirementNotUsed
		}
		var live int
		if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM pool_leases WHERE alias_id=? AND state IN ('leased','used','retiring')`, item.AliasID).Scan(&live); err != nil {
			return nil, err
		}
		if live != 1 {
			return nil, ErrPoolRetirementNotLive
		}
		alias, err := s.GetAlias(ctx, item.AliasID)
		if errors.Is(err, ErrNotFound) {
			return nil, ErrPoolRetirementAlias
		}
		if err != nil {
			return nil, err
		}
		if !alias.Enabled && strings.TrimSpace(alias.LastSyncError) == domain.AppleAliasConfirmationPending {
			return nil, ErrPoolRetirementPending
		}
		if !alias.Enabled || alias.CredentialMode != domain.AliasCredentialModeV2 || alias.CredentialVersion < 1 {
			return nil, ErrPoolRetirementNotICloud
		}
		account, err := s.GetAccount(ctx, alias.AccountID)
		if err != nil {
			return nil, err
		}
		if !account.Enabled || domain.NormalizeMailboxType(account.MailboxType) != domain.MailboxTypeICloud {
			return nil, ErrPoolRetirementNotICloud
		}
		var memberState string
		if err := s.queryRowContext(ctx, `SELECT state FROM pool_members WHERE alias_id=?`, item.AliasID).Scan(&memberState); err != nil || memberState != "used" {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, ErrPoolRetirementNotLive
			}
			if err != nil {
				return nil, err
			}
			return nil, ErrPoolRetirementNotLive
		}
		if _, exists := seen[alias.AccountID]; !exists {
			seen[alias.AccountID] = struct{}{}
			accounts = append(accounts, alias.AccountID)
		}
	}
	return accounts, nil
}

// StartPoolAliasRetirement performs whole-batch eligibility checks and moves
// every accepted used lease to retiring in the same short Pool transaction.
// Same client + operation ID + item order replays the existing durable job.
func (s *Store) StartPoolAliasRetirement(ctx context.Context, clientID, operationID, requestID string, items []PoolAliasRetirementItemInput) (PoolAliasRetirementJob, bool, error) {
	if err := validatePoolAliasRetirementInput(operationID, items); err != nil {
		return PoolAliasRetirementJob{}, false, err
	}
	fingerprint, err := poolAliasRetirementFingerprint(items)
	if err != nil {
		return PoolAliasRetirementJob{}, false, err
	}
	tx, err := s.poolTx(ctx)
	if err != nil {
		return PoolAliasRetirementJob{}, false, err
	}
	defer tx.Rollback()

	existing, existingErr := s.poolAliasRetirementJobTx(ctx, tx, operationID)
	if existingErr == nil {
		var existingFingerprint string
		if err := s.txQueryRowContext(ctx, tx, `SELECT fingerprint FROM pool_alias_retirement_jobs WHERE operation_id=?`, operationID).Scan(&existingFingerprint); err != nil {
			return PoolAliasRetirementJob{}, false, err
		}
		if existing.ClientID != clientID || existingFingerprint != fingerprint {
			return PoolAliasRetirementJob{}, false, ErrPoolRequestConflict
		}
		if err := tx.Commit(); err != nil {
			return PoolAliasRetirementJob{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(existingErr, ErrNotFound) {
		return PoolAliasRetirementJob{}, false, existingErr
	}

	var project string
	var active bool
	if err := s.txQueryRowContext(ctx, tx, `SELECT project,enabled FROM pool_clients WHERE id=?`, clientID).Scan(&project, &active); err != nil {
		return PoolAliasRetirementJob{}, false, err
	}
	if !active {
		return PoolAliasRetirementJob{}, false, ErrPoolClosed
	}

	job := PoolAliasRetirementJob{OperationID: operationID, ClientID: clientID, Project: project, Status: PoolAliasRetirementJobQueued, RequestID: requestID, Items: make([]PoolAliasRetirementJobItem, 0, len(items))}
	for ordinal, input := range items {
		lease, err := scanPoolLease(s.txQueryRowContext(ctx, tx, poolLeaseSelect+` WHERE l.id=?`, strings.TrimSpace(input.LeaseID)))
		if errors.Is(err, sql.ErrNoRows) {
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementLeaseNotFound
		}
		if err != nil {
			return PoolAliasRetirementJob{}, false, err
		}
		if lease.ClientID != clientID {
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementProject
		}
		if lease.AliasID != input.AliasID {
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementAlias
		}
		if lease.State != "used" {
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementNotUsed
		}
		var live int
		if err := s.txQueryRowContext(ctx, tx, `SELECT COUNT(*) FROM pool_leases WHERE alias_id=? AND state IN ('leased','used','retiring')`, input.AliasID).Scan(&live); err != nil {
			return PoolAliasRetirementJob{}, false, err
		}
		if live != 1 {
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementNotLive
		}
		alias, err := s.getAliasByIDTx(ctx, tx, input.AliasID)
		if errors.Is(err, ErrNotFound) {
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementAlias
		}
		if err != nil {
			return PoolAliasRetirementJob{}, false, err
		}
		if !alias.Enabled && strings.TrimSpace(alias.LastSyncError) == domain.AppleAliasConfirmationPending {
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementPending
		}
		if !alias.Enabled || alias.CredentialMode != domain.AliasCredentialModeV2 || alias.CredentialVersion < 1 {
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementNotICloud
		}
		var accountEnabled bool
		var mailboxType string
		if err := s.txQueryRowContext(ctx, tx, `SELECT a.enabled,COALESCE(ms.mailbox_type,'icloud')
			FROM accounts a LEFT JOIN account_mailbox_settings ms ON ms.account_id=a.id WHERE a.id=?`, alias.AccountID).Scan(&accountEnabled, &mailboxType); err != nil {
			return PoolAliasRetirementJob{}, false, err
		}
		if !accountEnabled || domain.NormalizeMailboxType(mailboxType) != domain.MailboxTypeICloud {
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementNotICloud
		}
		var memberState string
		if err := s.txQueryRowContext(ctx, tx, `SELECT state FROM pool_members WHERE alias_id=?`, input.AliasID).Scan(&memberState); err != nil || memberState != "used" {
			if errors.Is(err, sql.ErrNoRows) {
				return PoolAliasRetirementJob{}, false, ErrPoolRetirementNotLive
			}
			if err != nil {
				return PoolAliasRetirementJob{}, false, err
			}
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementNotLive
		}
		if _, err := s.lockAccountVersionForUpdate(ctx, tx, alias.AccountID); err != nil {
			return PoolAliasRetirementJob{}, false, err
		}
		job.Items = append(job.Items, PoolAliasRetirementJobItem{Ordinal: ordinal, AliasID: input.AliasID, LeaseID: lease.ID, AccountID: alias.AccountID, Address: alias.Address, State: "retiring"})
	}

	now := s.now().UTC()
	job.CreatedAt, job.UpdatedAt = now, now
	if _, err := s.txExecContext(ctx, tx, `INSERT INTO pool_alias_retirement_jobs(operation_id,client_id,project,fingerprint,status,request_id,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?)`, job.OperationID, job.ClientID, job.Project, fingerprint, job.Status, job.RequestID, timestamp(now), timestamp(now)); err != nil {
		if isUniqueConstraintError(err) {
			return PoolAliasRetirementJob{}, false, ErrPoolRequestConflict
		}
		return PoolAliasRetirementJob{}, false, err
	}
	for _, item := range job.Items {
		if _, err := s.txExecContext(ctx, tx, `INSERT INTO pool_alias_retirement_job_items(operation_id,ordinal,alias_id,lease_id,account_id,address,state,updated_at)
			VALUES(?,?,?,?,?,?,?,?)`, job.OperationID, item.Ordinal, item.AliasID, item.LeaseID, item.AccountID, item.Address, item.State, timestamp(now)); err != nil {
			return PoolAliasRetirementJob{}, false, err
		}
		result, err := s.txExecContext(ctx, tx, `UPDATE pool_leases SET state='retiring',updated_at=? WHERE id=? AND alias_id=? AND state='used'`, timestamp(now), item.LeaseID, item.AliasID)
		if err != nil {
			return PoolAliasRetirementJob{}, false, err
		}
		if err := requireAffected(result, "used pool lease"); err != nil {
			return PoolAliasRetirementJob{}, false, ErrPoolRetirementConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return PoolAliasRetirementJob{}, false, err
	}
	return job, true, nil
}

func (s *Store) poolAliasRetirementItemTx(ctx context.Context, tx *sql.Tx, operationID string, ordinal int) (PoolAliasRetirementJob, PoolAliasRetirementJobItem, error) {
	job, err := s.poolAliasRetirementJobTx(ctx, tx, operationID)
	if err != nil {
		return job, PoolAliasRetirementJobItem{}, err
	}
	for _, item := range job.Items {
		if item.Ordinal == ordinal {
			return job, item, nil
		}
	}
	return job, PoolAliasRetirementJobItem{}, ErrNotFound
}

// FinalizePoolAliasRetirement removes the local alias and pool membership only
// after the Apple deletion executor has confirmed its absence.
func (s *Store) FinalizePoolAliasRetirement(ctx context.Context, operationID string, ordinal int, aliasID int64) error {
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, item, err := s.poolAliasRetirementItemTx(ctx, tx, operationID, ordinal)
	if err != nil {
		return err
	}
	if item.State == "retired" {
		return tx.Commit()
	}
	if item.State != "retiring" || item.AliasID != aliasID {
		return ErrPoolRetirementConflict
	}
	lease, err := scanPoolLease(s.txQueryRowContext(ctx, tx, poolLeaseSelect+` WHERE l.id=?`, item.LeaseID))
	if err != nil || lease.State != "retiring" || lease.AliasID != item.AliasID {
		return ErrPoolRetirementConflict
	}
	alias, err := s.getAliasByIDTx(ctx, tx, item.AliasID)
	if err != nil || alias.AccountID != item.AccountID || domain.NormalizeEmail(alias.Address) != domain.NormalizeEmail(item.Address) {
		return ErrPoolRetirementConflict
	}
	accountVersion, err := s.lockAccountVersionForUpdate(ctx, tx, alias.AccountID)
	if err != nil {
		return err
	}
	now := s.now().UTC()
	result, err := s.txExecContext(ctx, tx, `DELETE FROM aliases WHERE id=? AND account_id=?`, alias.ID, alias.AccountID)
	if err != nil {
		return err
	}
	if err := requireAffected(result, "retiring alias"); err != nil {
		return ErrPoolRetirementConflict
	}
	if alias.Enabled {
		if _, err := s.bumpAccountVersionTx(ctx, tx, alias.AccountID, accountVersion); err != nil {
			return err
		}
	}
	result, err = s.txExecContext(ctx, tx, `UPDATE pool_leases SET alias_id=NULL,state='retired',updated_at=?
		WHERE id=? AND state='retiring'`, timestamp(now), item.LeaseID)
	if err != nil {
		return err
	}
	if err := requireAffected(result, "retiring pool lease"); err != nil {
		return ErrPoolRetirementConflict
	}
	if _, err = s.txExecContext(ctx, tx, `UPDATE pool_alias_retirement_job_items SET state='retired',result_code='',result_message='',updated_at=?
		WHERE operation_id=? AND ordinal=?`, timestamp(now), operationID, ordinal); err != nil {
		return err
	}
	return tx.Commit()
}

// RestorePoolAliasRetirement returns an explicitly failed Apple deletion to
// used. It refuses to revive a lease when its bound alias changed or vanished.
func (s *Store) RestorePoolAliasRetirement(ctx context.Context, operationID string, ordinal int, code, message string) error {
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, item, err := s.poolAliasRetirementItemTx(ctx, tx, operationID, ordinal)
	if err != nil {
		return err
	}
	if item.State == "used" {
		return tx.Commit()
	}
	if item.State != "retiring" {
		return ErrPoolRetirementConflict
	}
	lease, err := scanPoolLease(s.txQueryRowContext(ctx, tx, poolLeaseSelect+` WHERE l.id=?`, item.LeaseID))
	if err != nil || lease.State != "retiring" || lease.AliasID != item.AliasID {
		return ErrPoolRetirementConflict
	}
	alias, err := s.getAliasByIDTx(ctx, tx, item.AliasID)
	if err != nil || alias.AccountID != item.AccountID || domain.NormalizeEmail(alias.Address) != domain.NormalizeEmail(item.Address) {
		return ErrPoolRetirementConflict
	}
	if _, err := s.lockAccountVersionForUpdate(ctx, tx, alias.AccountID); err != nil {
		return err
	}
	var accountEnabled bool
	if err := s.txQueryRowContext(ctx, tx, `SELECT enabled FROM accounts WHERE id=?`, alias.AccountID).Scan(&accountEnabled); err != nil {
		return err
	}
	now := s.now().UTC()
	leaseResult, err := s.txExecContext(ctx, tx, `UPDATE pool_leases SET state='used',updated_at=? WHERE id=? AND state='retiring'`, timestamp(now), item.LeaseID)
	if err != nil {
		return err
	}
	if err := requireAffected(leaseResult, "retiring pool lease"); err != nil {
		return ErrPoolRetirementConflict
	}
	memberTable := "pool_members"
	if !accountEnabled {
		memberTable = "pool_suspended_members"
	}
	memberResult, err := s.txExecContext(ctx, tx, `UPDATE `+memberTable+` SET state='used',updated_at=? WHERE alias_id=?`, timestamp(now), item.AliasID)
	if err != nil {
		return err
	}
	if err := requireAffected(memberResult, "pool member"); err != nil {
		return ErrPoolRetirementConflict
	}
	if _, err := s.txExecContext(ctx, tx, `UPDATE pool_alias_retirement_job_items SET state='used',result_code=?,result_message=?,updated_at=?
		WHERE operation_id=? AND ordinal=?`, strings.TrimSpace(code), strings.TrimSpace(message), timestamp(now), operationID, ordinal); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkPoolAliasRetirementReview records an ambiguous or interrupted result.
// The lease intentionally remains retiring, which closes every Pool action.
func (s *Store) MarkPoolAliasRetirementReview(ctx context.Context, operationID string, ordinal int, code, message string) error {
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	job, item, err := s.poolAliasRetirementItemTx(ctx, tx, operationID, ordinal)
	if err != nil {
		return err
	}
	if item.State == "retired" || item.State == "used" {
		return tx.Commit()
	}
	now := s.now().UTC()
	if _, err := s.txExecContext(ctx, tx, `UPDATE pool_alias_retirement_job_items SET state='review',result_code=?,result_message=?,updated_at=?
		WHERE operation_id=? AND ordinal=?`, strings.TrimSpace(code), strings.TrimSpace(message), timestamp(now), operationID, ordinal); err != nil {
		return err
	}
	if job.Status != PoolAliasRetirementJobInterrupted {
		if _, err := s.txExecContext(ctx, tx, `UPDATE pool_alias_retirement_jobs SET status='review',updated_at=? WHERE operation_id=?`, timestamp(now), operationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetPoolAliasRetirementJobStatus(ctx context.Context, operationID, clientID, status string) error {
	switch status {
	case PoolAliasRetirementJobRunning, PoolAliasRetirementJobCompleted, PoolAliasRetirementJobReview, PoolAliasRetirementJobInterrupted:
	default:
		return ErrPoolInput
	}
	result, err := s.execContext(ctx, `UPDATE pool_alias_retirement_jobs SET status=?,updated_at=? WHERE operation_id=? AND client_id=?`, status, timestamp(s.now()), operationID, clientID)
	if err != nil {
		return err
	}
	return requireAffected(result, "pool alias retirement job")
}

// InterruptPoolAliasRetirementJobs is called once during process startup.
// Work that lost its in-memory executor remains reviewable and never replays.
func (s *Store) InterruptPoolAliasRetirementJobs(ctx context.Context) error {
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.now().UTC()
	if _, err := s.txExecContext(ctx, tx, `UPDATE pool_alias_retirement_job_items SET state='review',result_code='JOB_INTERRUPTED',
		result_message='删除任务在服务重启前中断，Apple 结果待人工核对',updated_at=?
		WHERE operation_id IN (SELECT operation_id FROM pool_alias_retirement_jobs WHERE status IN ('queued','running'))
		AND state='retiring'`, timestamp(now)); err != nil {
		return err
	}
	if _, err := s.txExecContext(ctx, tx, `UPDATE pool_alias_retirement_jobs SET status='interrupted',updated_at=? WHERE status IN ('queued','running')`, timestamp(now)); err != nil {
		return err
	}
	return tx.Commit()
}
