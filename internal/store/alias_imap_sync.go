package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"icloud-api/internal/domain"
)

// ErrAliasMailboxSyncStale means a targeted fetch was superseded before its
// publication; callers should retain the cache without reporting a fresh sync.
var ErrAliasMailboxSyncStale = errors.New("alias mailbox sync result is stale")

func (s *Store) migrateAliasIMAPSync(ctx context.Context, tx *sql.Tx) error {
	_, err := s.txExecContext(ctx, tx, `
		CREATE TABLE IF NOT EXISTS alias_imap_sync_states (
			alias_id BIGINT PRIMARY KEY REFERENCES aliases(id) ON DELETE CASCADE,
			uid_validity BIGINT NOT NULL,
			last_uid BIGINT NOT NULL,
			updated_at BIGINT NOT NULL
		)`)
	if err != nil {
		return fmt.Errorf("migrate alias IMAP sync states: %w", err)
	}
	return nil
}

// GetAliasIMAPSyncState reads the independent cursor used by targeted fetches.
// The account-wide cursor does not describe work observed for a single alias.
func (s *Store) GetAliasIMAPSyncState(ctx context.Context, aliasID int64) (domain.IMAPSyncState, error) {
	if aliasID < 1 {
		return domain.IMAPSyncState{}, fmt.Errorf("get alias IMAP sync state: alias ID must be positive")
	}
	var state domain.IMAPSyncState
	var uidValidity, lastUID, updatedAt int64
	err := s.queryRowContext(ctx, `
		SELECT a.account_id, state.uid_validity, state.last_uid, state.updated_at
		FROM alias_imap_sync_states state
		JOIN aliases a ON a.id = state.alias_id
		WHERE state.alias_id = ?`, aliasID,
	).Scan(&state.AccountID, &uidValidity, &lastUID, &updatedAt)
	if err == sql.ErrNoRows {
		return domain.IMAPSyncState{}, ErrNotFound
	}
	if err != nil {
		return domain.IMAPSyncState{}, fmt.Errorf("get alias IMAP sync state: %w", err)
	}
	if uidValidity < 1 || uidValidity > int64(^uint32(0)) || lastUID < 0 || lastUID > int64(^uint32(0)) {
		return domain.IMAPSyncState{}, fmt.Errorf(
			"get alias IMAP sync state: invalid mailbox position UIDVALIDITY=%d UID=%d", uidValidity, lastUID,
		)
	}
	state.UIDValidity = uint32(uidValidity)
	state.LastUID = uint32(lastUID)
	state.UpdatedAt = timeFromTimestamp(updatedAt)
	return state, nil
}

// ApplyAliasMailboxSync publishes only the target alias's messages, health and
// cursor. It never advances the account-wide cursor or another alias's state.
func (s *Store) ApplyAliasMailboxSync(
	ctx context.Context,
	expectedAccountVersion time.Time,
	alias domain.Alias,
	result domain.MailboxSyncResult,
	syncedAt time.Time,
) error {
	return s.applyAliasMailboxSync(ctx, expectedAccountVersion, alias, result, syncedAt, false)
}

// ApplyAliasWebMailboxSync publishes a bounded webmail observation without
// advancing the IMAP cursor. The caller's LastUID is only an observation key.
func (s *Store) ApplyAliasWebMailboxSync(
	ctx context.Context,
	expectedAccountVersion time.Time,
	alias domain.Alias,
	result domain.MailboxSyncResult,
	syncedAt time.Time,
) error {
	return s.applyAliasMailboxSync(ctx, expectedAccountVersion, alias, result, syncedAt, true)
}

func (s *Store) applyAliasMailboxSync(
	ctx context.Context,
	expectedAccountVersion time.Time,
	alias domain.Alias,
	result domain.MailboxSyncResult,
	syncedAt time.Time,
	webmail bool,
) error {
	defer s.cleanupArchiveInputs(result.ArchivedMessages)
	if expectedAccountVersion.IsZero() {
		return fmt.Errorf("apply alias mailbox sync: expected account version is required")
	}
	if err := validateMailboxSync(alias.AccountID, []domain.Alias{alias}, result); err != nil {
		return err
	}
	unlockArchive := s.lockMailArchiveAccount(alias.AccountID)
	defer unlockArchive()
	if webmail {
		messages, err := s.preserveExistingWebmailArchive(ctx, result.ArchivedMessages)
		if err != nil {
			return err
		}
		result.ArchivedMessages = messages
	}
	stagedArchive, err := s.stageArchiveMessages(result.ArchivedMessages)
	if err != nil {
		return fmt.Errorf("stage alias mailbox archive: %w", err)
	}
	archiveCommitted := false
	defer func() {
		if !archiveCommitted {
			s.cleanupPublishedArchive(stagedArchive)
		}
	}()
	if syncedAt.IsZero() {
		syncedAt = s.now()
	}
	syncedAt = syncedAt.UTC()
	observedAt := result.State.UpdatedAt.UTC()

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin alias mailbox sync: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	accountVersion, err := s.lockAccountVersionForUpdate(ctx, tx, alias.AccountID)
	if err != nil {
		return fmt.Errorf("lock account for alias mailbox sync: %w", err)
	}
	if accountVersion != timestamp(expectedAccountVersion) {
		return ErrAliasMailboxSyncStale
	}
	var accountEnabled, aliasEnabled bool
	var address string
	err = s.txQueryRowContext(ctx, tx, `
		SELECT account.enabled, a.enabled, a.address
		FROM aliases a JOIN accounts account ON account.id = a.account_id
		WHERE a.id = ? AND a.account_id = ?`, alias.ID, alias.AccountID,
	).Scan(&accountEnabled, &aliasEnabled, &address)
	if err == sql.ErrNoRows {
		return ErrAliasMailboxSyncStale
	}
	if err != nil {
		return fmt.Errorf("recheck alias before mailbox publish: %w", err)
	}
	if !accountEnabled || !aliasEnabled || address != alias.Address {
		return ErrAliasMailboxSyncStale
	}

	var currentUIDValidity, currentLastUID, currentUpdatedAt int64
	currentStateExists := false
	if !webmail {
		err = s.txQueryRowContext(ctx, tx, `
		SELECT uid_validity, last_uid, updated_at
		FROM alias_imap_sync_states WHERE alias_id = ?`, alias.ID,
		).Scan(&currentUIDValidity, &currentLastUID, &currentUpdatedAt)
		switch {
		case err == nil:
			currentStateExists = true
			currentObservedAt := timeFromTimestamp(currentUpdatedAt)
			sameGeneration := currentUIDValidity == int64(result.State.UIDValidity)
			if sameGeneration {
				if currentLastUID > int64(result.State.LastUID) {
					return ErrAliasMailboxSyncStale
				}
				if observedAt.IsZero() || observedAt.Before(currentObservedAt) {
					observedAt = currentObservedAt
				}
			} else {
				if !result.Reset {
					return ErrAliasMailboxSyncStale
				}
				if observedAt.IsZero() {
					observedAt = syncedAt
				}
				// UIDVALIDITY is opaque, so freshness rather than its numeric value
				// determines whether a different generation may replace this cursor.
				if !observedAt.After(currentObservedAt) {
					return ErrAliasMailboxSyncStale
				}
			}
		case err == sql.ErrNoRows:
			if !result.Reset {
				return ErrAliasMailboxSyncStale
			}
			if observedAt.IsZero() {
				observedAt = syncedAt
			}
		default:
			return fmt.Errorf("read alias IMAP sync state before publish: %w", err)
		}
	}

	if err := s.persistArchivedMessagesTx(ctx, tx, result.ArchivedMessages, stagedArchive, syncedAt); err != nil {
		return fmt.Errorf("persist alias mailbox archive: %w", err)
	}
	if webmail {
		if _, err := s.txExecContext(ctx, tx, `DELETE FROM latest_messages WHERE alias_id = ? AND uid_validity <> ?`, alias.ID, int64(result.State.UIDValidity)); err != nil {
			return fmt.Errorf("reset stale webmail legacy snapshot: %w", err)
		}
	}
	if result.Reset {
		query := `DELETE FROM latest_messages WHERE alias_id = ?`
		args := []any{alias.ID}
		if !currentStateExists || currentUIDValidity == int64(result.State.UIDValidity) {
			query += ` AND uid_validity <> ?`
			args = append(args, int64(result.State.UIDValidity))
		}
		if _, err := s.txExecContext(ctx, tx, query, args...); err != nil {
			return fmt.Errorf("reset alias legacy mailbox snapshot: %w", err)
		}
	}
	// The shared projection helper's reset is account-wide; reset only this
	// alias above, then use its ordinary dual-write and empty-snapshot paths.
	legacyResult := result
	legacyResult.Reset = false
	if err := s.persistLegacyLatestMessagesTx(ctx, tx, alias.AccountID, legacyResult, syncedAt, false); err != nil {
		return fmt.Errorf("persist alias legacy mailbox snapshot: %w", err)
	}
	if !webmail {
		if _, err := s.txExecContext(ctx, tx, `
		INSERT INTO alias_imap_sync_states(alias_id, uid_validity, last_uid, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(alias_id) DO UPDATE SET
			uid_validity = excluded.uid_validity,
			last_uid = excluded.last_uid,
			updated_at = excluded.updated_at`,
			alias.ID, int64(result.State.UIDValidity), int64(result.State.LastUID), timestamp(observedAt),
		); err != nil {
			return fmt.Errorf("upsert alias IMAP sync state: %w", err)
		}
	}
	syncStatus := domain.SyncStatusOK
	if result.HasMore {
		syncStatus = domain.SyncStatusPending
	}
	if _, err := s.txExecContext(ctx, tx, `
		UPDATE aliases
		SET last_sync_status = ?, last_sync_error = '', last_synced_at = ?, updated_at = ?
		WHERE id = ? AND account_id = ? AND enabled = TRUE`,
		syncStatus, timestamp(syncedAt), timestamp(syncedAt), alias.ID, alias.AccountID,
	); err != nil {
		return fmt.Errorf("mark mailbox alias synced: %w", err)
	}
	nextAccountVersion, err := s.nextAccountVersion(accountVersion)
	if err != nil {
		return fmt.Errorf("advance account version after alias mailbox sync: %w", err)
	}
	accountResult, err := s.txExecContext(ctx, tx, `
		UPDATE accounts
		SET last_sync_status = ?, last_sync_error = '', last_synced_at = ?, updated_at = ?
		WHERE id = ? AND updated_at = ?`,
		syncStatus, timestamp(syncedAt), nextAccountVersion, alias.AccountID, accountVersion,
	)
	if err != nil {
		return fmt.Errorf("mark alias mailbox account synced: %w", err)
	}
	if err := requireAffected(accountResult, "account"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit alias mailbox sync: %w", err)
	}
	archiveCommitted = true
	if err := s.EnforceMailArchiveLimit(ctx); err != nil {
		return fmt.Errorf("enforce alias mailbox archive limit: %w", err)
	}
	return nil
}

// preserveExistingWebmailArchive prevents a bounded web observation from
// replacing a complete IMAP MIME file for the same account/UID generation.
// The archive lock serializes this check with filesystem staging.
func (s *Store) preserveExistingWebmailArchive(ctx context.Context, messages []domain.ArchivedMessage) ([]domain.ArchivedMessage, error) {
	messages = append([]domain.ArchivedMessage(nil), messages...)
	for i := range messages {
		message := &messages[i]
		var state string
		var bodyTruncated bool
		err := s.queryRowContext(ctx, `SELECT content_state, body_truncated FROM archived_messages WHERE account_id = ? AND uid_validity = ? AND upstream_uid = ?`, message.AccountID, int64(message.UIDValidity), int64(message.UID)).Scan(&state, &bodyTruncated)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("check existing web mail archive: %w", err)
		}
		if err == nil && state == archiveContentAvailable {
			message.RawMIME = nil
			message.RawMIMEPath = ""
			message.RawSize = 0
			message.RawSHA256 = ""
			message.ContentState = archiveContentAvailable
			message.BodyTruncated = bodyTruncated
		}
	}
	return messages, nil
}
