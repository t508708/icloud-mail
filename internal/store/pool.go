package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"icloud-api/internal/domain"
)

var (
	ErrPoolEmpty           = errors.New("mailbox pool has insufficient available addresses")
	ErrPoolConflict        = errors.New("mailbox pool state conflict")
	ErrPoolRequestConflict = errors.New("idempotency key belongs to a different request")
	ErrPoolClosed          = errors.New("mailbox lease is closed or expired")
	ErrPoolInput           = errors.New("invalid mailbox pool input")
)

type PoolClient struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}

type PoolAccount struct {
	AccountID  int64  `json:"account_id"`
	Email      string `json:"email"`
	AutoEnroll bool   `json:"auto_enroll"`
	Target     int    `json:"target"`
	Available  int    `json:"available"`
}

type PoolMember struct {
	AliasID   int64     `json:"alias_id"`
	AccountID int64     `json:"account_id"`
	Address   string    `json:"address"`
	GroupName string    `json:"group_name"`
	State     string    `json:"state"`
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PoolLease struct {
	ID        string    `json:"id"`
	AliasID   int64     `json:"alias_id"`
	Address   string    `json:"address"`
	ClientID  string    `json:"client_id"`
	Project   string    `json:"project"`
	RequestID string    `json:"request_id"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PoolClaim struct {
	RequestID  string `json:"request_id"`
	Count      int    `json:"count"`
	AccountID  int64  `json:"account_id"`
	GroupID    int64  `json:"group_id"`
	TTLSeconds int    `json:"ttl_seconds"`
}

type PoolPage[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

func poolID() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func poolTokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// Pool tables have their own additive migration, independent of upstream v8.
func (s *Store) migratePool(ctx context.Context, tx *sql.Tx) error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS pool_lock (id INTEGER PRIMARY KEY, revision BIGINT NOT NULL)`,
		`INSERT INTO pool_lock(id, revision) VALUES (1, 0) ON CONFLICT(id) DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS pool_clients (id TEXT PRIMARY KEY, project TEXT NOT NULL UNIQUE,
		 token_hash TEXT NOT NULL UNIQUE, enabled BOOLEAN NOT NULL DEFAULT TRUE, created_at BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS pool_accounts (account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
		 auto_enroll BOOLEAN NOT NULL DEFAULT FALSE, target INTEGER NOT NULL DEFAULT 0 CHECK(target BETWEEN 0 AND 10000),
		 enroll_after BIGINT NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS pool_members (alias_id BIGINT PRIMARY KEY REFERENCES aliases(id) ON DELETE CASCADE,
		 state TEXT NOT NULL CHECK(state IN ('available','leased','used','paused')), updated_at BIGINT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS pool_requests (client_id TEXT NOT NULL REFERENCES pool_clients(id),
		 request_id TEXT NOT NULL, fingerprint TEXT NOT NULL, created_at BIGINT NOT NULL,
		 PRIMARY KEY(client_id, request_id))`,
		`CREATE TABLE IF NOT EXISTS pool_leases (id TEXT PRIMARY KEY, alias_id BIGINT REFERENCES aliases(id) ON DELETE SET NULL,
		 address TEXT NOT NULL, client_id TEXT NOT NULL REFERENCES pool_clients(id), request_id TEXT NOT NULL,
		 state TEXT NOT NULL CHECK(state IN ('leased','used','released','expired')),
		 created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL, updated_at BIGINT NOT NULL,
		 FOREIGN KEY(client_id,request_id) REFERENCES pool_requests(client_id,request_id))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS pool_one_live_lease ON pool_leases(alias_id) WHERE state IN ('leased','used')`,
		`CREATE INDEX IF NOT EXISTS pool_lease_expiry ON pool_leases(state, expires_at)`,
		`CREATE INDEX IF NOT EXISTS pool_lease_request ON pool_leases(client_id, request_id)`,
	} {
		if _, err := s.txExecContext(ctx, tx, query); err != nil {
			return fmt.Errorf("migrate mailbox pool: %w", err)
		}
	}
	return nil
}

// All pool writers lock this row before account rows. SQLite acquires its
// write lock before any reads; PostgreSQL serializes short allocation jobs.
func (s *Store) poolTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := s.txExecContext(ctx, tx, `UPDATE pool_lock SET revision = revision + 1 WHERE id = 1`); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (s *Store) CreatePoolClient(ctx context.Context, project string) (PoolClient, string, error) {
	project = strings.TrimSpace(project)
	if project == "" || len(project) > 80 {
		return PoolClient{}, "", ErrPoolInput
	}
	id, err := poolID()
	if err != nil {
		return PoolClient{}, "", err
	}
	secret, err := poolID()
	if err != nil {
		return PoolClient{}, "", err
	}
	token := "pool_" + secret
	client := PoolClient{ID: id, Project: project, Enabled: true, CreatedAt: s.now().UTC()}
	_, err = s.execContext(ctx, `INSERT INTO pool_clients(id,project,token_hash,enabled,created_at) VALUES(?,?,?,TRUE,?)`,
		id, project, poolTokenHash(token), timestamp(client.CreatedAt))
	if isUniqueConstraintError(err) {
		return PoolClient{}, "", ErrPoolConflict
	}
	return client, token, err
}

func (s *Store) ListPoolClients(ctx context.Context) ([]PoolClient, error) {
	rows, err := s.queryContext(ctx, `SELECT id,project,enabled,created_at FROM pool_clients ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PoolClient{}
	for rows.Next() {
		var v PoolClient
		var at int64
		if err := rows.Scan(&v.ID, &v.Project, &v.Enabled, &at); err != nil {
			return nil, err
		}
		v.CreatedAt = time.Unix(0, at).UTC()
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *Store) AuthenticatePoolClient(ctx context.Context, token string) (PoolClient, error) {
	var v PoolClient
	var at int64
	err := s.queryRowContext(ctx, `SELECT id,project,enabled,created_at FROM pool_clients WHERE token_hash = ? AND enabled = TRUE`, poolTokenHash(token)).Scan(&v.ID, &v.Project, &v.Enabled, &at)
	v.CreatedAt = time.Unix(0, at).UTC()
	return v, err
}

func (s *Store) UpdatePoolClient(ctx context.Context, id string, enabled bool, rotate bool) (string, error) {
	tx, err := s.poolTx(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	token := ""
	query := `UPDATE pool_clients SET enabled = ? WHERE id = ?`
	args := []any{enabled, id}
	if rotate {
		secret, err := poolID()
		if err != nil {
			return "", err
		}
		token = "pool_" + secret
		query = `UPDATE pool_clients SET enabled = ?, token_hash = ? WHERE id = ?`
		args = []any{enabled, poolTokenHash(token), id}
	}
	result, err := s.txExecContext(ctx, tx, query, args...)
	if err != nil {
		return "", err
	}
	if err := requireAffected(result, "pool client"); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

func (s *Store) ListPoolAccounts(ctx context.Context) ([]PoolAccount, error) {
	rows, err := s.queryContext(ctx, `SELECT a.id,a.email,COALESCE(p.auto_enroll,FALSE),COALESCE(p.target,0),
	 (SELECT COUNT(*) FROM pool_members m JOIN aliases al ON al.id=m.alias_id
	  WHERE al.account_id=a.id AND al.enabled=TRUE AND a.enabled=TRUE AND m.state='available')
	 FROM accounts a LEFT JOIN pool_accounts p ON p.account_id=a.id ORDER BY a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PoolAccount{}
	for rows.Next() {
		var v PoolAccount
		if err := rows.Scan(&v.AccountID, &v.Email, &v.AutoEnroll, &v.Target, &v.Available); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *Store) SetPoolAccount(ctx context.Context, id int64, autoEnroll bool, target int) error {
	if id < 1 || target < 0 || target > 10000 {
		return ErrPoolInput
	}
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = s.lockAccountVersionForUpdate(ctx, tx, id); err != nil {
		return err
	}
	_, err = s.txExecContext(ctx, tx, `INSERT INTO pool_accounts(account_id,auto_enroll,target,enroll_after)
	 VALUES(?,?,?,(SELECT COALESCE(MAX(id),0) FROM aliases))
	 ON CONFLICT(account_id) DO UPDATE SET auto_enroll=excluded.auto_enroll,target=excluded.target,
	 enroll_after=CASE WHEN pool_accounts.auto_enroll=FALSE AND excluded.auto_enroll=TRUE THEN excluded.enroll_after ELSE pool_accounts.enroll_after END`, id, autoEnroll, target)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) EnrollPoolAliases(ctx context.Context, ids []int64) error {
	if len(ids) == 0 || len(ids) > 1000 {
		return ErrPoolInput
	}
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		var eligible int
		err = s.txQueryRowContext(ctx, tx, `SELECT COUNT(*) FROM aliases al JOIN accounts a ON a.id=al.account_id
		 WHERE al.id=? AND al.enabled=TRUE AND a.enabled=TRUE AND al.credential_mode='v2' AND al.credential_version>0`, id).Scan(&eligible)
		if err != nil {
			return err
		}
		if eligible != 1 {
			return ErrPoolConflict
		}
		if _, err = s.txExecContext(ctx, tx, `INSERT INTO pool_members(alias_id,state,updated_at) VALUES(?,'available',?) ON CONFLICT(alias_id) DO NOTHING`, id, timestamp(s.now())); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetPoolMember(ctx context.Context, id int64, state string) error {
	if state != "available" && state != "paused" {
		return ErrPoolInput
	}
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := s.txExecContext(ctx, tx, `UPDATE pool_members SET state=?,updated_at=? WHERE alias_id=? AND state IN ('available','paused')`, state, timestamp(s.now()), id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrPoolConflict
	}
	return tx.Commit()
}

func (s *Store) ListPoolMembers(ctx context.Context, state, search string, limit, offset int) (PoolPage[PoolMember], error) {
	page := PoolPage[PoolMember]{Items: []PoolMember{}}
	where := ` FROM pool_members m JOIN aliases al ON al.id=m.alias_id JOIN accounts a ON a.id=al.account_id
	 LEFT JOIN mail_groups g ON g.id=al.group_id WHERE (?='' OR m.state=?) AND LOWER(al.address) LIKE ?`
	args := []any{state, state, "%" + strings.ToLower(search) + "%"}
	if err := s.queryRowContext(ctx, `SELECT COUNT(*)`+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := s.queryContext(ctx, `SELECT al.id,al.account_id,al.address,COALESCE(g.name,''),m.state,(al.enabled AND a.enabled),m.updated_at`+where+` ORDER BY al.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var v PoolMember
		var at int64
		if err := rows.Scan(&v.AliasID, &v.AccountID, &v.Address, &v.GroupName, &v.State, &v.Enabled, &at); err != nil {
			return page, err
		}
		v.UpdatedAt = time.Unix(0, at).UTC()
		page.Items = append(page.Items, v)
	}
	return page, rows.Err()
}

const poolLeaseSelect = `SELECT l.id,COALESCE(l.alias_id,0),l.address,l.client_id,c.project,l.request_id,l.state,l.created_at,l.expires_at,l.updated_at
 FROM pool_leases l JOIN pool_clients c ON c.id=l.client_id `

func scanPoolLease(row rowScanner) (PoolLease, error) {
	var v PoolLease
	var created, expires, updated int64
	err := row.Scan(&v.ID, &v.AliasID, &v.Address, &v.ClientID, &v.Project, &v.RequestID, &v.State, &created, &expires, &updated)
	v.CreatedAt = time.Unix(0, created).UTC()
	v.ExpiresAt = time.Unix(0, expires).UTC()
	v.UpdatedAt = time.Unix(0, updated).UTC()
	return v, err
}

func (s *Store) ListPoolLeases(ctx context.Context, clientID, state string, limit, offset int) (PoolPage[PoolLease], error) {
	page := PoolPage[PoolLease]{Items: []PoolLease{}}
	where := ` WHERE (?='' OR l.client_id=?) AND (?='' OR l.state=?)`
	args := []any{clientID, clientID, state, state}
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM pool_leases l`+where, args...).Scan(&page.Total); err != nil {
		return page, err
	}
	rows, err := s.queryContext(ctx, poolLeaseSelect+where+` ORDER BY l.created_at DESC,l.id LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanPoolLease(rows)
		if err != nil {
			return page, err
		}
		page.Items = append(page.Items, v)
	}
	return page, rows.Err()
}

func (s *Store) GetPoolLease(ctx context.Context, id, clientID string) (PoolLease, error) {
	return scanPoolLease(s.queryRowContext(ctx, poolLeaseSelect+` WHERE l.id=? AND (?='' OR l.client_id=?)`, id, clientID, clientID))
}

func (s *Store) poolRequestLeasesTx(ctx context.Context, tx *sql.Tx, clientID, requestID string) ([]PoolLease, error) {
	rows, err := s.txQueryContext(ctx, tx, poolLeaseSelect+` WHERE l.client_id=? AND l.request_id=? ORDER BY l.id`, clientID, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []PoolLease{}
	for rows.Next() {
		v, err := scanPoolLease(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *Store) ClaimPool(ctx context.Context, clientID string, input PoolClaim) ([]PoolLease, error) {
	if len(input.RequestID) < 8 || len(input.RequestID) > 128 || input.Count < 1 || input.Count > 50 || input.TTLSeconds < 60 || input.TTLSeconds > 86400 || input.AccountID < 0 || input.GroupID < 0 {
		return nil, ErrPoolInput
	}
	for _, r := range input.RequestID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return nil, ErrPoolInput
		}
	}
	fingerprint, _ := json.Marshal(input)
	tx, err := s.poolTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var active bool
	if err = s.txQueryRowContext(ctx, tx, `SELECT enabled FROM pool_clients WHERE id=?`, clientID).Scan(&active); err != nil {
		return nil, err
	}
	if !active {
		return nil, ErrPoolClosed
	}
	var previous string
	err = s.txQueryRowContext(ctx, tx, `SELECT fingerprint FROM pool_requests WHERE client_id=? AND request_id=?`, clientID, input.RequestID).Scan(&previous)
	if err == nil {
		if previous != string(fingerprint) {
			return nil, ErrPoolRequestConflict
		}
		return s.poolRequestLeasesTx(ctx, tx, clientID, input.RequestID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	rows, err := s.txQueryContext(ctx, tx, `SELECT al.id,al.account_id FROM pool_members m JOIN aliases al ON al.id=m.alias_id JOIN accounts a ON a.id=al.account_id
	 WHERE m.state='available' AND al.enabled=TRUE AND a.enabled=TRUE AND al.credential_mode='v2' AND al.credential_version>0
	 AND (?=0 OR a.id=?) AND (?=0 OR al.group_id=?) ORDER BY a.id,al.id LIMIT ?`, input.AccountID, input.AccountID, input.GroupID, input.GroupID, input.Count)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, account int64 }
	selected := []candidate{}
	for rows.Next() {
		var v candidate
		if err := rows.Scan(&v.id, &v.account); err != nil {
			rows.Close()
			return nil, err
		}
		selected = append(selected, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(selected) != input.Count {
		return nil, ErrPoolEmpty
	}
	now := s.now().UTC()
	if _, err = s.txExecContext(ctx, tx, `INSERT INTO pool_requests(client_id,request_id,fingerprint,created_at) VALUES(?,?,?,?)`, clientID, input.RequestID, string(fingerprint), timestamp(now)); err != nil {
		return nil, err
	}
	for _, v := range selected {
		if _, err = s.lockAccountVersionForUpdate(ctx, tx, v.account); err != nil {
			return nil, err
		}
		alias, err := s.getAliasByIDTx(ctx, tx, v.id)
		if err != nil {
			return nil, err
		}
		var enabled bool
		if err = s.txQueryRowContext(ctx, tx, `SELECT enabled FROM accounts WHERE id=?`, v.account).Scan(&enabled); err != nil {
			return nil, err
		}
		if !enabled || !alias.Enabled || alias.CredentialMode != domain.AliasCredentialModeV2 {
			return nil, ErrPoolConflict
		}
		if err = s.rotatePoolAliasTx(ctx, tx, alias); err != nil {
			return nil, err
		}
		id, err := poolID()
		if err != nil {
			return nil, err
		}
		if _, err = s.txExecContext(ctx, tx, `INSERT INTO pool_leases(id,alias_id,address,client_id,request_id,state,created_at,expires_at,updated_at) VALUES(?,?,?,?,?,'leased',?,?,?)`, id, v.id, alias.Address, clientID, input.RequestID, timestamp(now), timestamp(now.Add(time.Duration(input.TTLSeconds)*time.Second)), timestamp(now)); err != nil {
			return nil, err
		}
		if _, err = s.txExecContext(ctx, tx, `UPDATE pool_members SET state='leased',updated_at=? WHERE alias_id=?`, timestamp(now), v.id); err != nil {
			return nil, err
		}
	}
	result, err := s.poolRequestLeasesTx(ctx, tx, clientID, input.RequestID)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}

func (s *Store) rotatePoolAliasTx(ctx context.Context, tx *sql.Tx, alias domain.Alias) error {
	if s.credentialFactory == nil {
		return errors.New("pool requires credential issuer")
	}
	if _, err := s.installGeneratedAliasCredentialsTx(ctx, tx, alias.ID, alias.CredentialVersion+1, false); err != nil {
		return err
	}
	_, err := s.txExecContext(ctx, tx, `DELETE FROM pending_alias_api_keys WHERE alias_id=?`, alias.ID)
	return err
}

func (s *Store) ActPoolLease(ctx context.Context, id, clientID, action string, ttl int) (PoolLease, error) {
	if action != "commit" && action != "release" && action != "renew" && action != "expire" {
		return PoolLease{}, ErrPoolInput
	}
	if action == "renew" && (ttl < 60 || ttl > 86400) {
		return PoolLease{}, ErrPoolInput
	}
	tx, err := s.poolTx(ctx)
	if err != nil {
		return PoolLease{}, err
	}
	defer tx.Rollback()
	v, err := scanPoolLease(s.txQueryRowContext(ctx, tx, poolLeaseSelect+` WHERE l.id=? AND (?='' OR l.client_id=?)`, id, clientID, clientID))
	if err != nil {
		return v, err
	}
	now := s.now().UTC()
	if action == "release" && v.State == "released" || action == "commit" && v.State == "used" || action == "expire" && v.State == "expired" {
		return v, nil
	}
	if action == "expire" {
		if v.State != "leased" || now.Before(v.ExpiresAt) {
			return v, ErrPoolClosed
		}
	} else if v.State != "leased" || !now.Before(v.ExpiresAt) {
		return v, ErrPoolClosed
	}
	if v.AliasID == 0 {
		if action != "expire" && action != "release" {
			return v, ErrPoolClosed
		}
		v.State = "released"
		if action == "expire" {
			v.State = "expired"
		}
		v.UpdatedAt = now
		if _, err = s.txExecContext(ctx, tx, `UPDATE pool_leases SET state=?,updated_at=? WHERE id=?`, v.State, timestamp(now), v.ID); err != nil {
			return v, err
		}
		return v, tx.Commit()
	}
	var accountID int64
	if err = s.txQueryRowContext(ctx, tx, `SELECT account_id FROM aliases WHERE id=?`, v.AliasID).Scan(&accountID); err != nil {
		return v, err
	}
	if _, err = s.lockAccountVersionForUpdate(ctx, tx, accountID); err != nil {
		return v, err
	}
	alias, err := s.getAliasByIDTx(ctx, tx, v.AliasID)
	if err != nil {
		return v, err
	}
	memberState := "leased"
	switch action {
	case "commit":
		v.State = "used"
		memberState = "used"
	case "renew":
		v.ExpiresAt = now.Add(time.Duration(ttl) * time.Second)
	case "release", "expire":
		if err = s.rotatePoolAliasTx(ctx, tx, alias); err != nil {
			return v, err
		}
		memberState = "available"
		v.State = "released"
		if action == "expire" {
			v.State = "expired"
		}
	}
	v.UpdatedAt = now
	if _, err = s.txExecContext(ctx, tx, `UPDATE pool_leases SET state=?,expires_at=?,updated_at=? WHERE id=?`, v.State, timestamp(v.ExpiresAt), timestamp(now), v.ID); err != nil {
		return v, err
	}
	if _, err = s.txExecContext(ctx, tx, `UPDATE pool_members SET state=?,updated_at=? WHERE alias_id=?`, memberState, timestamp(now), v.AliasID); err != nil {
		return v, err
	}
	return v, tx.Commit()
}

// RefreshPool adds only addresses created after opting an account into the pool.
// Existing addresses are enrolled explicitly so previously used mail stays out.
func (s *Store) RefreshPool(ctx context.Context) error {
	tx, err := s.poolTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = s.txExecContext(ctx, tx, `INSERT INTO pool_members(alias_id,state,updated_at)
	 SELECT al.id,'available',? FROM aliases al JOIN pool_accounts p ON p.account_id=al.account_id
	 JOIN accounts a ON a.id=al.account_id WHERE p.auto_enroll=TRUE AND al.id>p.enroll_after
	 AND al.enabled=TRUE AND a.enabled=TRUE AND al.credential_mode='v2' AND al.credential_version>0
	 ON CONFLICT(alias_id) DO NOTHING`, timestamp(s.now()))
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	rows, err := s.queryContext(ctx, `SELECT id FROM pool_leases WHERE state='leased' AND expires_at<=? ORDER BY expires_at LIMIT 200`, timestamp(s.now()))
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = s.ActPoolLease(ctx, id, "", "expire", 0); err != nil && !errors.Is(err, ErrPoolClosed) && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}

// PoolCreationAllowed is an optional scheduler gate. The plan keeps advancing
// while stocked, so resuming after a claim preserves the hourly pacing.
func (s *Store) PoolCreationAllowed(ctx context.Context, accountID int64) (bool, error) {
	var target int
	var auto bool
	var after int64
	err := s.queryRowContext(ctx, `SELECT auto_enroll,target,enroll_after FROM pool_accounts WHERE account_id=?`, accountID).Scan(&auto, &target, &after)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !auto || target == 0 {
		return true, nil
	}
	var count int
	err = s.queryRowContext(ctx, `SELECT COUNT(*) FROM aliases al LEFT JOIN pool_members m ON m.alias_id=al.id
	 WHERE al.account_id=? AND al.enabled=TRUE AND al.credential_mode='v2' AND al.credential_version>0
	 AND (m.state='available' OR (m.alias_id IS NULL AND al.id>?))`, accountID, after).Scan(&count)
	return count < target, err
}
