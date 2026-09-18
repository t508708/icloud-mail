package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

func TestPoolAliasRetirementPostgresMigrationConvergesAcrossVersions(t *testing.T) {
	t.Parallel()

	for _, version := range []int{0, 3, 4, 5, 6, 7, 8} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			t.Parallel()
			capture := &postgresMigrationCaptureDriver{version: version}
			driverName := fmt.Sprintf("icloud-api-pool-retirement-postgres-migrate-%p", capture)
			sql.Register(driverName, capture)
			raw, err := sql.Open(driverName, "")
			if err != nil {
				t.Fatal(err)
			}
			raw.SetMaxOpenConns(1)
			t.Cleanup(func() { _ = raw.Close() })
			store := newStore(raw, dialectPostgres)

			for pass := range 2 {
				capture.statements = nil
				if err := store.Migrate(context.Background()); err != nil {
					t.Fatalf("PostgreSQL convergence pass %d: %v", pass, err)
				}
				for _, statement := range []string{
					`CREATE TABLE IF NOT EXISTS pool_alias_retirement_jobs ( operation_id TEXT PRIMARY KEY`,
					`CREATE TABLE IF NOT EXISTS pool_alias_retirement_job_items ( operation_id TEXT NOT NULL REFERENCES pool_alias_retirement_jobs(operation_id) ON DELETE CASCADE`,
					`ALTER TABLE pool_leases ADD CONSTRAINT pool_leases_state_check CHECK(state IN ('leased','used','retiring','retired','released','expired'))`,
					`CREATE UNIQUE INDEX pool_one_live_lease ON pool_leases(alias_id) WHERE state IN ('leased','used','retiring')`,
				} {
					if !containsNormalizedSQLFragment(capture.statements, statement) {
						t.Errorf("PostgreSQL pass %d omitted pool retirement convergence: %s", pass, normalizeSQL(statement))
					}
				}
				capture.version = 8
			}
		})
	}
}

func containsNormalizedSQLFragment(statements []string, wanted string) bool {
	wanted = normalizeSQL(wanted)
	for _, statement := range statements {
		if strings.Contains(normalizeSQL(statement), wanted) {
			return true
		}
	}
	return false
}
