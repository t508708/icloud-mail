package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"icloud-api/internal/domain"
)

var ErrInvalidAccountProxy = errors.New("invalid account proxy configuration")

func (s *Store) migrateAccountProxy(ctx context.Context, tx *sql.Tx) error {
	_, err := s.txExecContext(ctx, tx, `CREATE TABLE IF NOT EXISTS account_proxy_configs (
		account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
		enabled BOOLEAN NOT NULL DEFAULT FALSE,
		endpoint_ciphertext TEXT NOT NULL DEFAULT '',
		upstream_ciphertext TEXT NOT NULL DEFAULT '',
		revision BIGINT NOT NULL DEFAULT 1,
		updated_at BIGINT NOT NULL DEFAULT 0
	)`)
	if err != nil {
		return fmt.Errorf("create account proxy config: %w", err)
	}
	return nil
}

// The parent account transaction serializes configuration changes with edits.
func (s *Store) saveAccountProxyConfigTx(ctx context.Context, tx *sql.Tx, accountID int64, config domain.AccountProxyConfig) error {
	if config.Enabled && config.EndpointCiphertext == "" {
		return ErrInvalidAccountProxy
	}
	var prior domain.AccountProxyConfig
	err := s.txQueryRowContext(ctx, tx, `SELECT enabled, endpoint_ciphertext, upstream_ciphertext, revision
		FROM account_proxy_configs WHERE account_id = ?`, accountID).
		Scan(&prior.Enabled, &prior.EndpointCiphertext, &prior.UpstreamCiphertext, &prior.Revision)
	if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("load account proxy: %w", err)
	}
	if err == nil && prior.Enabled == config.Enabled && prior.EndpointCiphertext == config.EndpointCiphertext && prior.UpstreamCiphertext == config.UpstreamCiphertext {
		return nil
	}
	_, err = s.txExecContext(ctx, tx, `INSERT INTO account_proxy_configs
		(account_id, enabled, endpoint_ciphertext, upstream_ciphertext, revision, updated_at)
		VALUES (?, ?, ?, ?, 1, ?)
		ON CONFLICT(account_id) DO UPDATE SET enabled = excluded.enabled,
		endpoint_ciphertext = excluded.endpoint_ciphertext,
		upstream_ciphertext = excluded.upstream_ciphertext,
		revision = account_proxy_configs.revision + 1,
		updated_at = excluded.updated_at`,
		accountID, config.Enabled, config.EndpointCiphertext, config.UpstreamCiphertext, timestamp(s.now()))
	if err != nil {
		return fmt.Errorf("save account proxy: %w", err)
	}
	return nil
}
