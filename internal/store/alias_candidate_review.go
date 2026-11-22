package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"icloud-api/internal/domain"
)

// ReviewStaleAccountAliasCandidate preserves an uncertain candidate while
// releasing its creation-channel gate. The caller must first obtain two valid,
// complete directories that both omit this exact address.
func (s *Store) ReviewStaleAccountAliasCandidate(ctx context.Context, accountID, aliasID int64, address string, notNewerThan time.Time) error {
	address = domain.NormalizeEmail(address)
	if accountID < 1 || aliasID < 1 || address == "" || notNewerThan.IsZero() {
		return fmt.Errorf("review stale alias candidate: identity and deadline are required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin alias candidate review: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	version, err := s.lockAccountVersionForUpdate(ctx, tx, accountID)
	if err != nil {
		return fmt.Errorf("lock account before candidate review: %w", err)
	}
	// Serialize against the remote-ID checkpoint as well as account writers.
	locked, err := s.txExecContext(ctx, tx, `UPDATE apple_alias_creation_candidates SET remote_id = remote_id
		WHERE alias_id = ? AND channel = 'apple_account' AND remote_id = ''`, aliasID)
	if err != nil {
		return fmt.Errorf("lock candidate provenance before review: %w", err)
	}
	if err := requireAffected(locked, "review candidate provenance"); err != nil {
		return ErrAliasConfirmationPending
	}
	var eligible int
	err = s.txQueryRowContext(ctx, tx, `SELECT COUNT(*) FROM aliases al
		JOIN pending_alias_api_keys p ON p.alias_id = al.id
		JOIN apple_alias_creation_candidates c ON c.alias_id = al.id
		WHERE al.id = ? AND al.account_id = ? AND al.address = ?
		AND al.enabled = FALSE AND al.last_sync_error = ?
		AND al.created_at <= ? AND p.created_at <= ?
		AND c.channel = 'apple_account' AND c.remote_id = ''`,
		aliasID, accountID, address, domain.AppleAliasConfirmationPending,
		timestamp(notNewerThan.UTC()), timestamp(notNewerThan.UTC())).Scan(&eligible)
	if err != nil {
		return fmt.Errorf("check alias candidate review eligibility: %w", err)
	}
	if eligible != 1 {
		return ErrAliasConfirmationPending
	}
	result, err := s.txExecContext(ctx, tx, `INSERT INTO apple_alias_candidate_reviews(alias_id, reviewed_at)
		VALUES (?, ?) ON CONFLICT (alias_id) DO NOTHING`, aliasID, timestamp(s.now().UTC()))
	if err != nil {
		return fmt.Errorf("record alias candidate review: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read alias candidate review result: %w", err)
	}
	if changed > 0 {
		if _, err := s.bumpAccountVersionTx(ctx, tx, accountID, version); err != nil {
			return fmt.Errorf("advance account version after candidate review: %w", err)
		}
	}
	return tx.Commit()
}

// CountAliasCreationOccupancy includes unresolved candidates, even when review
// has removed their creation-channel gate.
func (s *Store) CountAliasCreationOccupancy(ctx context.Context, accountID int64) (int, error) {
	if accountID < 1 {
		return 0, fmt.Errorf("count alias creation occupancy: account ID must be positive")
	}
	var count int
	err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM aliases WHERE account_id = ?
		AND (enabled = TRUE OR (enabled = FALSE AND last_sync_error = ?))`,
		accountID, domain.AppleAliasConfirmationPending).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count alias creation occupancy: %w", err)
	}
	return count, nil
}

// ListReviewPendingAliases retains original encrypted credentials for exact
// directory reconciliation; these keys remain withheld from issuance.
func (s *Store) ListReviewPendingAliases(ctx context.Context, accountID int64) ([]domain.PendingAliasAPIKey, error) {
	if accountID < 1 {
		return nil, fmt.Errorf("list review candidates: account ID must be positive")
	}
	rows, err := s.queryContext(ctx, `SELECT `+aliasColumns+`, p.api_key_ciphertext, p.created_at`+aliasJoins+`
		JOIN pending_alias_api_keys p ON p.alias_id = al.id
		JOIN apple_alias_candidate_reviews r ON r.alias_id = al.id
		WHERE al.account_id = ? AND al.enabled = FALSE AND al.last_sync_error = ?
		ORDER BY p.created_at, al.id`, accountID, domain.AppleAliasConfirmationPending)
	if err != nil {
		return nil, fmt.Errorf("list review candidates: %w", err)
	}
	result := make([]domain.PendingAliasAPIKey, 0)
	for rows.Next() {
		pending, err := scanPendingAliasAPIKey(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		result = append(result, pending)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return nil, fmt.Errorf("iterate review candidates: %w", iterationErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	// Close rows before provenance reads, including on single-connection SQLite.
	for i := range result {
		err := s.queryRowContext(ctx, `SELECT channel, remote_id FROM apple_alias_creation_candidates WHERE alias_id = ?`, result[i].ID).
			Scan(&result[i].CreationChannel, &result[i].CreationRemoteID)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("read review candidate provenance: %w", err)
		}
	}
	return result, nil
}

// ConfirmReviewedAliasFromDirectory publishes the original candidate after the
// caller verified its exact active directory entry and forwarding target.
// It deliberately leaves both channel sessions and original credentials intact.
func (s *Store) ConfirmReviewedAliasFromDirectory(ctx context.Context, accountID int64, address string) (domain.Alias, error) {
	address = domain.NormalizeEmail(address)
	if accountID < 1 || address == "" {
		return domain.Alias{}, fmt.Errorf("confirm reviewed alias: identity is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return domain.Alias{}, fmt.Errorf("begin reviewed alias confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	version, err := s.lockAccountVersionForUpdate(ctx, tx, accountID)
	if err != nil {
		return domain.Alias{}, err
	}
	state, err := s.readAccountMailboxStateTx(ctx, tx, accountID)
	if err != nil {
		return domain.Alias{}, err
	}
	if !state.Enabled {
		return domain.Alias{}, ErrAccountDisabled
	}
	if state.MailboxType != domain.MailboxTypeICloud {
		return domain.Alias{}, ErrICloudMailboxRequired
	}
	var aliasID int64
	err = s.txQueryRowContext(ctx, tx, `SELECT al.id FROM aliases al
		JOIN apple_alias_candidate_reviews r ON r.alias_id = al.id
		JOIN pending_alias_api_keys p ON p.alias_id = al.id
		WHERE al.account_id = ? AND al.address = ? AND al.enabled = FALSE AND al.last_sync_error = ?`,
		accountID, address, domain.AppleAliasConfirmationPending).Scan(&aliasID)
	if err == sql.ErrNoRows {
		return domain.Alias{}, ErrNotFound
	}
	if err != nil {
		return domain.Alias{}, fmt.Errorf("read reviewed alias before confirmation: %w", err)
	}
	if err := s.requirePendingAutoAliasConfirmationCapacity(ctx, tx, accountID); err != nil {
		return domain.Alias{}, err
	}
	result, err := s.txExecContext(ctx, tx, `UPDATE aliases SET enabled = TRUE,
		last_sync_status = ?, last_sync_error = '', last_synced_at = NULL, updated_at = ?
		WHERE id = ? AND account_id = ? AND enabled = FALSE AND last_sync_error = ?`,
		domain.SyncStatusPending, timestamp(s.now().UTC()), aliasID, accountID, domain.AppleAliasConfirmationPending)
	if err != nil {
		return domain.Alias{}, fmt.Errorf("enable reviewed alias: %w", err)
	}
	if err := requireAffected(result, "reviewed alias"); err != nil {
		return domain.Alias{}, err
	}
	if _, err := s.txExecContext(ctx, tx, `DELETE FROM apple_alias_candidate_reviews WHERE alias_id = ?`, aliasID); err != nil {
		return domain.Alias{}, err
	}
	if _, err := s.bumpAccountVersionTx(ctx, tx, accountID, version); err != nil {
		return domain.Alias{}, err
	}
	if err := s.autoEnrollAliasTx(ctx, tx, accountID, aliasID, true); err != nil {
		return domain.Alias{}, err
	}
	alias, err := scanAlias(s.txQueryRowContext(ctx, tx, `SELECT `+aliasColumns+aliasJoins+` WHERE al.id = ?`, aliasID))
	if err != nil {
		return domain.Alias{}, err
	}
	if err := s.recordAliasCreationEventTx(ctx, tx, alias); err != nil {
		return domain.Alias{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Alias{}, err
	}
	return alias, nil
}
