package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type AliasCreationJobEntry struct {
	AliasID   int64     `json:"alias_id"`
	Address   string    `json:"address"`
	Channel   string    `json:"channel"`
	CreatedAt time.Time `json:"created_at"`
}

type AliasCreationJob struct {
	ID        string                  `json:"id"`
	AccountID int64                   `json:"account_id"`
	Target    int                     `json:"target"`
	Completed int                     `json:"completed"`
	Channel   string                  `json:"channel"`
	Status    string                  `json:"status"`
	LastError string                  `json:"last_error,omitempty"`
	NextRunAt *time.Time              `json:"next_run_at,omitempty"`
	Entries   []AliasCreationJobEntry `json:"entries"`
	CreatedAt time.Time               `json:"created_at"`
	UpdatedAt time.Time               `json:"updated_at"`
}

var ErrAliasCreationJobConflict = errors.New("alias creation job conflict")

func (s *Store) EnsureAliasCreationJobs(ctx context.Context) error {
	_, err := s.execContext(ctx, `CREATE TABLE IF NOT EXISTS alias_creation_jobs (
 id TEXT PRIMARY KEY, account_id BIGINT NOT NULL, target INTEGER NOT NULL CHECK(target BETWEEN 1 AND 100),
 completed INTEGER NOT NULL DEFAULT 0, channel TEXT NOT NULL, status TEXT NOT NULL,
 last_error TEXT NOT NULL DEFAULT '', next_run_at BIGINT, entries_json TEXT NOT NULL DEFAULT '[]',
 created_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
 FOREIGN KEY(account_id) REFERENCES accounts(id) ON DELETE CASCADE)`)
	if err != nil {
		return fmt.Errorf("create alias creation jobs: %w", err)
	}
	_, err = s.execContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS alias_creation_jobs_active_account
 ON alias_creation_jobs(account_id) WHERE status IN ('running','waiting')`)
	return err
}

func (s *Store) InterruptAliasCreationJobs(ctx context.Context) error {
	_, err := s.execContext(ctx, `UPDATE alias_creation_jobs SET status='interrupted', next_run_at=NULL, updated_at=? WHERE status IN ('running','waiting')`, timestamp(s.now()))
	return err
}

func (s *Store) CreateAliasCreationJob(ctx context.Context, j AliasCreationJob) error {
	if j.CreatedAt.IsZero() {
		j.CreatedAt = s.now()
	}
	if j.UpdatedAt.IsZero() {
		j.UpdatedAt = j.CreatedAt
	}
	b, err := json.Marshal(j.Entries)
	if err != nil {
		return err
	}
	_, err = s.execContext(ctx, `INSERT INTO alias_creation_jobs(id,account_id,target,completed,channel,status,last_error,next_run_at,entries_json,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.AccountID, j.Target, j.Completed, j.Channel, j.Status, j.LastError, nullableCreationTimestamp(j.NextRunAt), string(b), timestamp(j.CreatedAt), timestamp(j.UpdatedAt))
	if err != nil {
		if _, activeErr := s.GetActiveAliasCreationJob(ctx, j.AccountID); activeErr == nil {
			return ErrAliasCreationJobConflict
		}
		return fmt.Errorf("insert alias creation job: %w", err)
	}
	return nil
}

func (s *Store) SaveAliasCreationJob(ctx context.Context, j AliasCreationJob) error {
	b, err := json.Marshal(j.Entries)
	if err != nil {
		return err
	}
	j.UpdatedAt = s.now()
	result, err := s.execContext(ctx, `UPDATE alias_creation_jobs SET completed=?,status=?,last_error=?,next_run_at=?,entries_json=?,updated_at=? WHERE id=?`, j.Completed, j.Status, j.LastError, nullableCreationTimestamp(j.NextRunAt), string(b), timestamp(j.UpdatedAt), j.ID)
	if err != nil {
		return err
	}
	return requireAffected(result, "alias creation job")
}

func (s *Store) GetLatestAliasCreationJob(ctx context.Context, accountID int64) (AliasCreationJob, error) {
	return s.getAliasCreationJob(ctx, `WHERE account_id=? ORDER BY created_at DESC LIMIT 1`, accountID)
}
func (s *Store) GetActiveAliasCreationJob(ctx context.Context, accountID int64) (AliasCreationJob, error) {
	return s.getAliasCreationJob(ctx, `WHERE account_id=? AND status IN ('running','waiting') LIMIT 1`, accountID)
}
func (s *Store) StopAliasCreationJob(ctx context.Context, accountID int64) error {
	_, err := s.execContext(ctx, `UPDATE alias_creation_jobs SET status='stopped',next_run_at=NULL,updated_at=? WHERE account_id=? AND status IN ('running','waiting')`, timestamp(s.now()), accountID)
	return err
}
func (s *Store) getAliasCreationJob(ctx context.Context, suffix string, accountID int64) (AliasCreationJob, error) {
	var j AliasCreationJob
	var next, created, updated int64
	var b string
	err := s.queryRowContext(ctx, `SELECT id,account_id,target,completed,channel,status,last_error,COALESCE(next_run_at,0),entries_json,created_at,updated_at FROM alias_creation_jobs `+suffix, accountID).Scan(&j.ID, &j.AccountID, &j.Target, &j.Completed, &j.Channel, &j.Status, &j.LastError, &next, &b, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	if err != nil {
		return j, err
	}
	if err := json.Unmarshal([]byte(b), &j.Entries); err != nil {
		return j, fmt.Errorf("decode alias creation job entries: %w", err)
	}
	j.CreatedAt = timeFromTimestamp(created)
	j.UpdatedAt = timeFromTimestamp(updated)
	if next != 0 {
		x := timeFromTimestamp(next)
		j.NextRunAt = &x
	}
	return j, nil
}

func nullableCreationTimestamp(t *time.Time) any {
	if t == nil {
		return nil
	}
	return timestamp(*t)
}
