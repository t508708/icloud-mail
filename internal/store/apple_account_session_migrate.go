package store

import (
	"context"
	"database/sql"
)

// migrateAppleAccountSession creates the independent account-session channel.
func migrateAppleAccountSession(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS apple_account_sessions (
  account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  session_ciphertext TEXT NOT NULL CHECK(length(trim(session_ciphertext)) > 0),
  apple_id TEXT NOT NULL CHECK(length(trim(apple_id)) > 0),
  region TEXT NOT NULL DEFAULT '',
  authenticated BOOLEAN NOT NULL DEFAULT FALSE,
  last_validated_at BIGINT,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL
 )`)
	return err
}
