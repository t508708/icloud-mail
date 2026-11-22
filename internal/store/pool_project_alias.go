package store

import (
	"context"
	"database/sql"
	"errors"

	"icloud-api/internal/domain"
)

// PoolProjectAlias records usage by a project without transferring ownership
// of the mailbox, rotating credentials, or altering existing pool leases.
type PoolProjectAlias struct {
	AliasID    int64  `json:"alias_id"`
	AccountID  int64  `json:"account_id"`
	Address    string `json:"address"`
	Source     string `json:"source"`
	APIKeyHash []byte `json:"-"`
}

// LinkPoolProjectAccountAliases links all usable iCloud aliases in one account
// to a project. Repeating the operation keeps existing records unchanged.
func (s *Store) LinkPoolProjectAccountAliases(ctx context.Context, clientID string, accountID int64) (linked, added int, err error) {
	if clientID == "" || accountID <= 0 {
		return 0, 0, ErrPoolInput
	}
	tx, err := s.poolTx(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	var enabled bool
	if err := s.txQueryRowContext(ctx, tx, `SELECT enabled FROM pool_clients WHERE id=?`, clientID).Scan(&enabled); errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound
	} else if err != nil {
		return 0, 0, err
	}
	if !enabled {
		return 0, 0, ErrPoolClosed
	}
	if _, err := s.lockAccountVersionForUpdate(ctx, tx, accountID); err != nil {
		return 0, 0, err
	}
	var accountEnabled bool
	var mailboxType string
	if err := s.txQueryRowContext(ctx, tx, `SELECT a.enabled,COALESCE(ms.mailbox_type,'icloud')
		FROM accounts a LEFT JOIN account_mailbox_settings ms ON ms.account_id=a.id WHERE a.id=?`, accountID).Scan(&accountEnabled, &mailboxType); err != nil {
		return 0, 0, err
	}
	if !accountEnabled || domain.NormalizeMailboxType(mailboxType) != domain.MailboxTypeICloud {
		return 0, 0, ErrPoolConflict
	}
	// An Apple deletion in progress must not acquire new project references.
	const eligible = `al.account_id=? AND al.enabled=TRUE AND al.credential_mode='v2'
		AND al.credential_version>0 AND al.credential_ciphertext<>''
		AND COALESCE(al.last_sync_error,'')<>'APPLE_ALIAS_CONFIRMATION_PENDING'
		AND NOT EXISTS (SELECT 1 FROM pool_leases in_flight WHERE in_flight.alias_id=al.id AND in_flight.state IN ('leased','retiring'))`
	if err := s.txQueryRowContext(ctx, tx, `SELECT COUNT(*) FROM aliases al WHERE `+eligible, accountID).Scan(&linked); err != nil {
		return 0, 0, err
	}
	result, err := s.txExecContext(ctx, tx, `INSERT INTO pool_project_aliases(client_id,alias_id,created_at)
		SELECT ?,al.id,? FROM aliases al WHERE `+eligible+`
		ON CONFLICT(client_id,alias_id) DO NOTHING`, clientID, timestamp(s.now()), accountID)
	if err != nil {
		return 0, 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return linked, int(n), nil
}

// ListPoolProjectAliases includes both explicit sharing and existing used
// leases, returning one row per address even when both relationships exist.
func (s *Store) ListPoolProjectAliases(ctx context.Context, clientID string, accountID int64, limit, offset int) (PoolPage[PoolProjectAlias], error) {
	page := PoolPage[PoolProjectAlias]{Items: []PoolProjectAlias{}}
	if clientID == "" || accountID < 0 || limit < 1 || limit > 1000 || offset < 0 || offset > 1000000 {
		return page, ErrPoolInput
	}
	var projectID string
	if err := s.queryRowContext(ctx, `SELECT id FROM pool_clients WHERE id=?`, clientID).Scan(&projectID); errors.Is(err, sql.ErrNoRows) {
		return page, ErrNotFound
	} else if err != nil {
		return page, err
	}
	const where = ` FROM aliases al JOIN accounts a ON a.id=al.account_id
		LEFT JOIN account_mailbox_settings ms ON ms.account_id=a.id
		WHERE a.enabled=TRUE AND al.enabled=TRUE AND al.credential_mode='v2'
		AND al.credential_version>0 AND al.credential_ciphertext<>''
		AND COALESCE(al.last_sync_error,'')<>'APPLE_ALIAS_CONFIRMATION_PENDING'
		AND COALESCE(ms.mailbox_type,'icloud')='icloud'
		AND (?=0 OR a.id=?)
		AND NOT EXISTS (SELECT 1 FROM pool_leases retiring WHERE retiring.alias_id=al.id AND retiring.state='retiring')
		AND (EXISTS (SELECT 1 FROM pool_project_aliases shared WHERE shared.client_id=? AND shared.alias_id=al.id)
		OR EXISTS (SELECT 1 FROM pool_leases used WHERE used.client_id=? AND used.alias_id=al.id AND used.state='used'))`
	args := []any{accountID, accountID, clientID, clientID}
	if err := s.queryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := s.queryContext(ctx, `SELECT al.id,al.account_id,al.address,
		CASE WHEN EXISTS (SELECT 1 FROM pool_leases used WHERE used.client_id=? AND used.alias_id=al.id AND used.state='used')
		THEN 'claimed' ELSE 'shared' END, al.api_key_hash`+where+` ORDER BY al.account_id,al.id LIMIT ? OFFSET ?`,
		append(append([]any{clientID}, args...), limit, offset)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var alias PoolProjectAlias
		if err := rows.Scan(&alias.AliasID, &alias.AccountID, &alias.Address, &alias.Source, &alias.APIKeyHash); err != nil {
			return page, err
		}
		page.Items = append(page.Items, alias)
	}
	return page, rows.Err()
}

// UnlinkPoolProjectAlias only removes explicit use, never a claimed lease.
func (s *Store) UnlinkPoolProjectAlias(ctx context.Context, clientID string, aliasID int64) error {
	if clientID == "" || aliasID <= 0 {
		return ErrPoolInput
	}
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := s.txExecContext(ctx, tx, `DELETE FROM pool_project_aliases WHERE client_id=? AND alias_id=?`, clientID, aliasID)
	if err != nil {
		return err
	}
	if err := requireAffected(result, "shared project alias"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) poolAliasHasOtherProjectUsers(ctx context.Context, aliasID int64, clientID string) (bool, error) {
	var exists bool
	err := s.queryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pool_project_aliases
		WHERE alias_id=? AND client_id<>?)`, aliasID, clientID).Scan(&exists)
	return exists, err
}

func (s *Store) poolAliasHasOtherProjectUsersTx(ctx context.Context, tx *sql.Tx, aliasID int64, clientID string) (bool, error) {
	var exists bool
	err := s.txQueryRowContext(ctx, tx, `SELECT EXISTS(SELECT 1 FROM pool_project_aliases
		WHERE alias_id=? AND client_id<>?)`, aliasID, clientID).Scan(&exists)
	return exists, err
}
