package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"icloud-api/internal/domain"
)

// OTPQueryFilter constrains OTP lookups for per-project and sub-address (+tag)
// isolation without altering default unfiltered responses.
type OTPQueryFilter struct {
	After         *time.Time
	SenderFilter  string
	SubjectFilter string
	Tag           string
	Limit         int
}

type DashboardAccountStats struct {
	Total                 int `json:"total"`
	Enabled               int `json:"enabled"`
	Disabled              int `json:"disabled"`
	IMAPHealthy           int `json:"imap_healthy"`
	IMAPAuthPaused        int `json:"imap_auth_paused"`
	AppleWebConnected     int `json:"apple_web_connected"`
	AppleAccountConnected int `json:"apple_account_connected"`
	AutoCreateEnabled     int `json:"auto_create_enabled"`
	ProxyEnabled          int `json:"proxy_enabled"`
}

type DashboardAliasStats struct {
	Total           int `json:"total"`
	Enabled         int `json:"enabled"`
	WithMail        int `json:"with_mail"`
	CreatedLastHour int `json:"created_last_hour"`
	CreatedToday    int `json:"created_today"`
}

type DashboardPoolStats struct {
	Available       int `json:"available"`
	Leased          int `json:"leased"`
	Used            int `json:"used"`
	Paused          int `json:"paused"`
	SharedLinks     int `json:"shared_links"`
	ProjectsTotal   int `json:"projects_total"`
	ProjectsEnabled int `json:"projects_enabled"`
}

type DashboardArchiveStats struct {
	ContentBytes int64 `json:"content_bytes"`
	ContentLimit int64 `json:"content_limit"`
	MessageCount int64 `json:"message_count"`
	EvictedCount int64 `json:"evicted_count"`
}

type AttentionAccount struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Reason string `json:"reason"`
	Detail string `json:"detail"`
}

type DashboardSummary struct {
	Accounts          DashboardAccountStats `json:"accounts"`
	Aliases           DashboardAliasStats   `json:"aliases"`
	Pool              DashboardPoolStats    `json:"pool"`
	Archive           DashboardArchiveStats `json:"archive"`
	AttentionAccounts []AttentionAccount    `json:"attention_accounts"`
	GeneratedAt       time.Time             `json:"generated_at"`
}

func (s *Store) migrateEnterpriseExtensions(ctx context.Context, tx *sql.Tx) error {
	if s.dialect == dialectPostgres {
		for _, stmt := range []string{
			`ALTER TABLE pool_clients ADD COLUMN IF NOT EXISTS sender_filter TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE pool_clients ADD COLUMN IF NOT EXISTS subject_filter TEXT NOT NULL DEFAULT ''`,
			`ALTER TABLE aliases ADD COLUMN IF NOT EXISTS auto_enroll_eligible BOOLEAN NOT NULL DEFAULT TRUE`,
		} {
			if _, err := s.txExecContext(ctx, tx, stmt); err != nil {
				return fmt.Errorf("migrate postgres enterprise columns: %w", err)
			}
		}
	} else {
		if err := s.ensureSQLiteColumn(ctx, tx, "pool_clients", "sender_filter", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		if err := s.ensureSQLiteColumn(ctx, tx, "pool_clients", "subject_filter", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		if err := s.ensureSQLiteColumn(ctx, tx, "aliases", "auto_enroll_eligible", "INTEGER NOT NULL DEFAULT 1"); err != nil {
			return err
		}
	}

	for _, idx := range []string{
		`CREATE INDEX IF NOT EXISTS pool_members_state_alias_idx ON pool_members(state, alias_id)`,
		`CREATE INDEX IF NOT EXISTS pool_leases_client_state_created_idx ON pool_leases(client_id, state, created_at DESC, id)`,
		`CREATE INDEX IF NOT EXISTS archived_messages_content_path_idx ON archived_messages(content_path)`,
		`CREATE INDEX IF NOT EXISTS alias_messages_alias_created_idx ON alias_messages(alias_id, created_at DESC, message_id DESC)`,
		`CREATE INDEX IF NOT EXISTS audit_logs_action_result_idx ON audit_logs(action, result, created_at DESC)`,
	} {
		if _, err := s.txExecContext(ctx, tx, idx); err != nil {
			return fmt.Errorf("create enterprise index: %w", err)
		}
	}
	return nil
}

func (s *Store) ensureSQLiteColumn(ctx context.Context, tx *sql.Tx, table, column, definition string) error {
	rows, err := s.txQueryContext(ctx, tx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return fmt.Errorf("inspect sqlite table %s: %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		if strings.EqualFold(name, column) {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	if _, err := s.txExecContext(ctx, tx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)); err != nil {
		return fmt.Errorf("add sqlite column %s.%s: %w", table, column, err)
	}
	return nil
}

func normalizePoolFilterRule(raw string) string {
	raw = sanitizePostgresText(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}
	replacer := strings.NewReplacer("\r\n", ",", "\n", ",", ";", ",", "，", ",", "；", ",")
	raw = replacer.Replace(raw)
	seen := make(map[string]struct{})
	var tokens []string
	totalLen := 0
	for part := range strings.SplitSeq(raw, ",") {
		item := strings.ToLower(strings.TrimSpace(part))
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		nextLen := len(item)
		if len(tokens) > 0 {
			nextLen++
		}
		if totalLen+nextLen > 500 {
			break
		}
		seen[item] = struct{}{}
		tokens = append(tokens, item)
		totalLen += nextLen
	}
	return strings.Join(tokens, ",")
}

func splitFilterTokens(raw string) []string {
	raw = normalizePoolFilterRule(raw)
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

// autoEnrollAliasTx immediately enrolls a newly created or newly confirmed
// iCloud alias into pool_members when the owning account is enabled and has
// auto_enroll turned on. Callers must already hold lockAccountVersionForUpdate.
func (s *Store) autoEnrollAliasTx(ctx context.Context, tx *sql.Tx, accountID, aliasID int64, bypassWatermark bool) error {
	if accountID < 1 || aliasID < 1 {
		return nil
	}
	var autoEnroll bool
	var enrollAfter int64
	err := s.txQueryRowContext(ctx, tx, `
		SELECT p.auto_enroll, p.enroll_after
		FROM pool_accounts p
		JOIN accounts a ON a.id = p.account_id
		LEFT JOIN account_mailbox_settings ms ON ms.account_id = a.id
		WHERE p.account_id = ? AND a.enabled = TRUE AND COALESCE(ms.mailbox_type, 'icloud') = 'icloud'`,
		accountID,
	).Scan(&autoEnroll, &enrollAfter)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read auto-enroll state: %w", err)
	}
	if !autoEnroll {
		return nil
	}
	if !bypassWatermark && aliasID <= enrollAfter {
		return nil
	}
	_, err = s.txExecContext(ctx, tx, `
		INSERT INTO pool_members(alias_id, state, updated_at)
		SELECT al.id, 'available', ?
		FROM aliases al
		WHERE al.id = ? AND al.account_id = ?
		  AND al.enabled = TRUE
		  AND al.auto_enroll_eligible = TRUE
		  AND al.credential_mode = 'v2' AND al.credential_version > 0
		  AND COALESCE(al.last_sync_error, '') <> 'APPLE_ALIAS_CONFIRMATION_PENDING'
		  AND `+poolUnconsumedAliasSQL+` AND `+poolUnsharedAliasSQL+`
		ON CONFLICT(alias_id) DO NOTHING`,
		timestamp(s.now()), aliasID, accountID,
	)
	if err != nil {
		return fmt.Errorf("auto-enroll alias %d: %w", aliasID, err)
	}
	return nil
}

// GetPoolClient returns a single pool client by ID.
func (s *Store) GetPoolClient(ctx context.Context, id string) (PoolClient, error) {
	var v PoolClient
	var at int64
	err := s.queryRowContext(ctx, `
		SELECT id, project, enabled, sender_filter, subject_filter, created_at
		FROM pool_clients WHERE id = ?`, strings.TrimSpace(id),
	).Scan(&v.ID, &v.Project, &v.Enabled, &v.SenderFilter, &v.SubjectFilter, &at)
	if err != nil {
		return PoolClient{}, err
	}
	v.CreatedAt = time.Unix(0, at).UTC()
	return v, nil
}

// VerifyPoolProjectOTPAccess checks that clientID is enabled and currently has
// an active shared link or live lease for aliasID, returning the client and its
// configured sender/subject OTP filters.
func (s *Store) VerifyPoolProjectOTPAccess(ctx context.Context, clientID string, aliasID int64) (PoolClient, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" || aliasID < 1 {
		return PoolClient{}, ErrNotFound
	}
	client, err := s.GetPoolClient(ctx, clientID)
	if err != nil {
		return PoolClient{}, err
	}
	if !client.Enabled {
		return PoolClient{}, ErrNotFound
	}
	nowTS := timestamp(s.now())
	var authorized bool
	err = s.queryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM pool_project_aliases WHERE client_id = ? AND alias_id = ?
		) OR EXISTS(
			SELECT 1 FROM pool_leases
			WHERE client_id = ? AND alias_id = ?
			  AND (state = 'used' OR (state = 'leased' AND expires_at > ?))
		)`, clientID, aliasID, clientID, aliasID, nowTS,
	).Scan(&authorized)
	if err != nil {
		return PoolClient{}, err
	}
	if !authorized {
		return PoolClient{}, ErrNotFound
	}
	return client, nil
}

// ListAliasOTPsFiltered queries OTP records for aliasID while optionally
// filtering by receive time, sender domain/keyword, subject keyword, and +tag.
func (s *Store) ListAliasOTPsFiltered(
	ctx context.Context,
	aliasID int64,
	rootAddress string,
	filter OTPQueryFilter,
) ([]domain.OTPRecord, error) {
	limit := filter.Limit
	if limit < 1 || limit > maxOTPHistory {
		limit = maxOTPHistory
	}
	senderTokens := splitFilterTokens(filter.SenderFilter)
	subjectTokens := splitFilterTokens(filter.SubjectFilter)
	tag := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(filter.Tag, "+")))
	hasMetadataFilter := len(senderTokens) > 0 || len(subjectTokens) > 0 || tag != ""

	afterTS := int64(0)
	hasAfter := filter.After != nil && !filter.After.IsZero()
	if hasAfter {
		afterTS = timestamp(filter.After.UTC())
	}

	if !hasMetadataFilter && !hasAfter {
		return s.ListAliasOTPs(ctx, aliasID, limit)
	}

	scanLimit := maxOTPHistory
	if hasMetadataFilter {
		scanLimit = 300
	}
	query := `
		SELECT am.otp, m.internal_date, m.from_json, m.to_json, m.cc_json, m.subject
		FROM alias_messages am
		JOIN archived_messages m ON m.id = am.message_id
		WHERE am.alias_id = ? AND am.otp <> ''`
	args := []any{aliasID}
	if hasAfter {
		query += ` AND m.internal_date > ?`
		args = append(args, afterTS)
	}
	query += ` ORDER BY am.created_at DESC, am.message_id DESC LIMIT ?`
	args = append(args, scanLimit)

	rows, err := s.queryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list filtered alias OTPs: %w", err)
	}
	defer rows.Close()

	result := make([]domain.OTPRecord, 0, min(limit, 16))
	for rows.Next() {
		var otp, fromJSON, toJSON, ccJSON, subject string
		var ts int64
		if err := rows.Scan(&otp, &ts, &fromJSON, &toJSON, &ccJSON, &subject); err != nil {
			return nil, fmt.Errorf("scan filtered alias OTP: %w", err)
		}
		if !domain.IsSixDigitOTP(otp) {
			continue
		}
		if hasMetadataFilter && !messageMatchesOTPFilter(rootAddress, fromJSON, toJSON, ccJSON, subject, senderTokens, subjectTokens, tag) {
			continue
		}
		result = append(result, domain.OTPRecord{
			OTP:  otp,
			Time: timeFromTimestamp(ts),
		})
		if len(result) >= limit {
			break
		}
	}
	return result, rows.Err()
}

// GetLatestAliasOTPFiltered returns the newest valid 6-digit OTP strictly after
// filter.After that also satisfies optional project/sender/subject/+tag rules.
func (s *Store) GetLatestAliasOTPFiltered(
	ctx context.Context,
	aliasID int64,
	rootAddress string,
	filter OTPQueryFilter,
) (domain.OTPRecord, error) {
	if filter.SenderFilter == "" && filter.SubjectFilter == "" && strings.TrimSpace(filter.Tag) == "" && filter.After != nil {
		return s.GetLatestAliasOTPAfter(ctx, aliasID, *filter.After)
	}
	filter.Limit = 1
	records, err := s.ListAliasOTPsFiltered(ctx, aliasID, rootAddress, filter)
	if err != nil {
		return domain.OTPRecord{}, err
	}
	if len(records) == 0 {
		return domain.OTPRecord{}, ErrNotFound
	}
	return records[0], nil
}

func messageMatchesOTPFilter(
	rootAddress, fromJSON, toJSON, ccJSON, subject string,
	senderTokens, subjectTokens []string,
	tag string,
) bool {
	if len(senderTokens) > 0 {
		var senders []domain.MailAddress
		_ = json.Unmarshal([]byte(fromJSON), &senders)
		matched := false
		for _, sender := range senders {
			email := strings.ToLower(strings.TrimSpace(sender.Email))
			name := strings.ToLower(strings.TrimSpace(sender.Name))
			for _, token := range senderTokens {
				if token != "" && (strings.Contains(email, token) || strings.Contains(name, token)) {
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(subjectTokens) > 0 {
		lowerSubject := strings.ToLower(subject)
		matched := false
		for _, token := range subjectTokens {
			if token != "" && strings.Contains(lowerSubject, token) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if tag != "" {
		var toList, ccList []domain.MailAddress
		_ = json.Unmarshal([]byte(toJSON), &toList)
		_ = json.Unmarshal([]byte(ccJSON), &ccList)
		if !recipientMatchesPlusTag(rootAddress, tag, toList, ccList) {
			return false
		}
	}
	return true
}

func recipientMatchesPlusTag(rootAddress, wantTag string, groups ...[]domain.MailAddress) bool {
	root := domain.NormalizeEmail(rootAddress)
	at := strings.LastIndexByte(root, '@')
	if at <= 0 {
		return false
	}
	wantEmail := root[:at] + "+" + strings.ToLower(strings.TrimSpace(wantTag)) + root[at:]
	for _, group := range groups {
		for _, item := range group {
			if domain.NormalizeEmail(item.Email) == wantEmail {
				return true
			}
		}
	}
	return false
}

// ListRecentArchivedMailboxMessages returns the newest archived messages for an
// alias (default 5, max 20) along with the total archived message count.
func (s *Store) ListRecentArchivedMailboxMessages(
	ctx context.Context,
	aliasID int64,
	limit int,
) ([]domain.ArchivedMailboxMessage, int, error) {
	if aliasID < 1 {
		return nil, 0, ErrPoolInput
	}
	if limit < 1 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	var total int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM alias_messages WHERE alias_id = ?`, aliasID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count archived mailbox messages: %w", err)
	}
	if total == 0 {
		return []domain.ArchivedMailboxMessage{}, 0, nil
	}
	rows, err := s.queryContext(ctx, `
		SELECT `+archivedMailboxColumns+`
		FROM alias_messages am
		JOIN aliases al ON al.id = am.alias_id
		JOIN archived_messages m ON m.id = am.message_id
		WHERE am.alias_id = ?
		ORDER BY m.internal_date DESC, am.mailbox_uid DESC
		LIMIT ?`, aliasID, limit,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list recent archived mailbox messages: %w", err)
	}
	defer rows.Close()
	items := make([]domain.ArchivedMailboxMessage, 0, limit)
	for rows.Next() {
		item, scanErr := scanArchivedMailboxMessage(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// HasArchivedContentFile checks file presence and exact byte length via os.Stat
// without reading and hashing the entire file body.
func (s *Store) HasArchivedContentFile(message domain.ArchivedMailboxMessage) bool {
	if message.ContentState != archiveContentAvailable || message.ContentBytes < 1 {
		return false
	}
	path, ok := s.safeArchivePath(message.ContentPath)
	if !ok {
		return false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != message.ContentBytes {
		return false
	}
	return true
}

// OpenArchivedContentForPreview opens an archived .eml file after validating
// its path and byte size, avoiding full-file SHA-256 scans on large attachments.
func (s *Store) OpenArchivedContentForPreview(message domain.ArchivedMailboxMessage) (*os.File, error) {
	path, ok := s.safeArchivePath(message.ContentPath)
	if !ok || message.ContentState != archiveContentAvailable {
		return nil, ErrNotFound
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.markArchivedContentMissing(message.ID)
			return nil, ErrNotFound
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != message.ContentBytes {
		_ = file.Close()
		s.markArchivedContentMissing(message.ID)
		return nil, ErrNotFound
	}
	return file, nil
}

// LookupAliasForInbox resolves either an exact registered alias address or a
// root+tag@domain address whose root@domain is registered.
func (s *Store) LookupAliasForInbox(ctx context.Context, rawAddress string) (domain.Alias, string, error) {
	normalized := domain.NormalizeEmail(sanitizePostgresText(rawAddress))
	if normalized == "" || !strings.Contains(normalized, "@") {
		return domain.Alias{}, "", ErrNotFound
	}
	alias, err := s.GetAliasByAddress(ctx, normalized)
	if err == nil {
		return alias, "", nil
	}
	if !errors.Is(err, ErrNotFound) {
		return domain.Alias{}, "", err
	}
	at := strings.LastIndexByte(normalized, '@')
	if at <= 0 {
		return domain.Alias{}, "", ErrNotFound
	}
	local, domainPart := normalized[:at], normalized[at+1:]
	plus := strings.IndexByte(local, '+')
	if plus <= 0 || domainPart == "" {
		return domain.Alias{}, "", ErrNotFound
	}
	rootAddr := local[:plus] + "@" + domainPart
	tag := strings.TrimSpace(local[plus+1:])
	rootAlias, rootErr := s.GetAliasByAddress(ctx, rootAddr)
	if rootErr != nil {
		return domain.Alias{}, "", rootErr
	}
	return rootAlias, tag, nil
}

// SuggestInboxAliases returns up to limit enabled-account aliases matching a
// partial address or label query, prioritizing aliases that have received mail.
func (s *Store) SuggestInboxAliases(ctx context.Context, query string, limit int) ([]domain.Alias, error) {
	if limit < 1 {
		limit = 10
	}
	if limit > 30 {
		limit = 30
	}
	query = strings.TrimSpace(sanitizePostgresText(query))
	if at := strings.LastIndexByte(query, '@'); at > 0 {
		if plus := strings.IndexByte(query[:at], '+'); plus > 0 {
			query = query[:plus] + query[at:]
		}
	}
	where := ` WHERE ac.enabled = TRUE AND COALESCE(al.last_sync_error, '') <> 'APPLE_ALIAS_CONFIRMATION_PENDING'`
	var args []any
	if query != "" {
		pattern := "%" + escapeLikePattern(query) + "%"
		where += ` AND (LOWER(al.address) LIKE LOWER(?) ESCAPE '!' OR LOWER(al.label) LIKE LOWER(?) ESCAPE '!')`
		args = append(args, pattern, pattern)
	}
	args = append(args, limit)
	rows, err := s.queryContext(ctx, `
		SELECT `+aliasColumns+aliasJoins+where+`
		ORDER BY CASE WHEN EXISTS (SELECT 1 FROM alias_messages am WHERE am.alias_id = al.id) THEN 0 ELSE 1 END,
		         al.updated_at DESC, al.id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("suggest inbox aliases: %w", err)
	}
	defer rows.Close()
	items := make([]domain.Alias, 0, limit)
	for rows.Next() {
		alias, scanErr := scanAlias(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, alias)
	}
	return items, rows.Err()
}

// GetDashboardSummary aggregates real-time operational health across accounts,
// aliases, mailbox pool, and disk archive storage.
func (s *Store) GetDashboardSummary(ctx context.Context, now, todayStart, recentSince time.Time) (DashboardSummary, error) {
	summary := DashboardSummary{
		AttentionAccounts: []AttentionAccount{},
		GeneratedAt:       now.UTC(),
	}

	// 1. Account statistics & attention radar
	rows, err := s.queryContext(ctx, `
		SELECT a.id, a.name, a.email, a.enabled, a.last_sync_status, a.last_sync_error,
		       COALESCE(ms.mailbox_type, 'icloud'),
		       COALESCE(ws.authenticated, FALSE),
		       COALESCE(acs.authenticated, FALSE),
		       COALESCE(sch.enabled, FALSE),
		       COALESCE(px.enabled, FALSE),
		       EXISTS (SELECT 1 FROM account_auto_login_states als WHERE als.account_id = a.id AND als.status = 'needs_action')
		FROM accounts a
		LEFT JOIN account_mailbox_settings ms ON ms.account_id = a.id
		LEFT JOIN apple_web_sessions ws ON ws.account_id = a.id
		LEFT JOIN apple_account_sessions acs ON acs.account_id = a.id
		LEFT JOIN alias_creation_schedules sch ON sch.account_id = a.id
		LEFT JOIN account_proxy_configs px ON px.account_id = a.id
		ORDER BY a.id`)
	if err != nil {
		return summary, fmt.Errorf("dashboard account query: %w", err)
	}
	for rows.Next() {
		var id int64
		var name, email, syncStatus, syncErr, mailboxType string
		var enabled, webAuth, acctAuth, autoCreate, proxyOn, autoLoginNeedsAction bool
		if scanErr := rows.Scan(&id, &name, &email, &enabled, &syncStatus, &syncErr, &mailboxType, &webAuth, &acctAuth, &autoCreate, &proxyOn, &autoLoginNeedsAction); scanErr != nil {
			_ = rows.Close()
			return summary, scanErr
		}
		summary.Accounts.Total++
		if !enabled {
			summary.Accounts.Disabled++
			continue
		}
		summary.Accounts.Enabled++
		if proxyOn {
			summary.Accounts.ProxyEnabled++
		}
		if autoCreate {
			summary.Accounts.AutoCreateEnabled++
		}
		isICloud := domain.NormalizeMailboxType(mailboxType) == domain.MailboxTypeICloud
		if isICloud && webAuth {
			summary.Accounts.AppleWebConnected++
		}
		if isICloud && acctAuth {
			summary.Accounts.AppleAccountConnected++
		}
		if domain.IsIMAPAuthenticationFailure(syncErr) {
			summary.Accounts.IMAPAuthPaused++
			if len(summary.AttentionAccounts) < 12 {
				summary.AttentionAccounts = append(summary.AttentionAccounts, AttentionAccount{
					ID:     id,
					Name:   name,
					Email:  email,
					Reason: "imap_auth_paused",
					Detail: "IMAP 收件认证已暂停，请更新 Apple App 专用密码",
				})
			}
		} else {
			if syncStatus == domain.SyncStatusOK || syncErr == "" {
				summary.Accounts.IMAPHealthy++
			}
			if syncErr != "" && len(summary.AttentionAccounts) < 12 {
				summary.AttentionAccounts = append(summary.AttentionAccounts, AttentionAccount{
					ID:     id,
					Name:   name,
					Email:  email,
					Reason: "sync_error",
					Detail: "收件同步存在异常：" + truncate(syncErr, 96),
				})
			} else if isICloud && autoLoginNeedsAction && len(summary.AttentionAccounts) < 12 {
				summary.AttentionAccounts = append(summary.AttentionAccounts, AttentionAccount{
					ID:     id,
					Name:   name,
					Email:  email,
					Reason: "auto_login_action",
					Detail: "自动保活连续受阻已暂停自动重登，请检查 Apple 凭据或接码服务",
				})
			} else if isICloud && !webAuth && len(summary.AttentionAccounts) < 12 {
				summary.AttentionAccounts = append(summary.AttentionAccounts, AttentionAccount{
					ID:     id,
					Name:   name,
					Email:  email,
					Reason: "apple_auth_required",
					Detail: "iCloud Web 目录连接未登录或已过期，无法同步/创建隐藏邮箱",
				})
			}
		}
	}
	if err := rows.Close(); err != nil {
		return summary, err
	}

	// 2. Alias & creation metrics
	if err := s.queryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN al.enabled = TRUE AND ac.enabled = TRUE THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN EXISTS (SELECT 1 FROM alias_messages am WHERE am.alias_id = al.id) THEN 1 ELSE 0 END), 0)
		FROM aliases al
		JOIN accounts ac ON ac.id = al.account_id
		WHERE COALESCE(al.last_sync_error, '') <> 'APPLE_ALIAS_CONFIRMATION_PENDING'`,
	).Scan(&summary.Aliases.Total, &summary.Aliases.Enabled, &summary.Aliases.WithMail); err != nil {
		return summary, fmt.Errorf("dashboard alias stats: %w", err)
	}
	_ = s.queryRowContext(ctx, `SELECT COUNT(*) FROM apple_alias_creation_events WHERE created_at >= ?`, timestamp(recentSince.UTC())).Scan(&summary.Aliases.CreatedLastHour)
	_ = s.queryRowContext(ctx, `SELECT COUNT(*) FROM apple_alias_creation_events WHERE created_at >= ?`, timestamp(todayStart.UTC())).Scan(&summary.Aliases.CreatedToday)

	// 3. Pool metrics
	_ = s.queryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN m.state = 'available' AND `+poolClaimableAliasSQL+` THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN m.state = 'leased' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN m.state = 'used' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN m.state = 'paused' THEN 1 ELSE 0 END), 0)
		FROM pool_members m
		JOIN aliases al ON al.id = m.alias_id
		JOIN accounts a ON a.id = al.account_id
		LEFT JOIN account_mailbox_settings ms ON ms.account_id = a.id
		WHERE COALESCE(ms.mailbox_type, 'icloud') = 'icloud'`,
	).Scan(&summary.Pool.Available, &summary.Pool.Leased, &summary.Pool.Used, &summary.Pool.Paused)
	_ = s.queryRowContext(ctx, `SELECT COUNT(*) FROM pool_project_aliases`).Scan(&summary.Pool.SharedLinks)
	_ = s.queryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN enabled = TRUE THEN 1 ELSE 0 END), 0)
		FROM pool_clients`,
	).Scan(&summary.Pool.ProjectsTotal, &summary.Pool.ProjectsEnabled)

	// 4. Archive storage metrics
	archiveStats, err := s.MailArchiveStats(ctx)
	if err == nil {
		summary.Archive = DashboardArchiveStats{
			ContentBytes: archiveStats.ContentBytes,
			ContentLimit: archiveStats.ContentLimit,
			MessageCount: archiveStats.MessageCount,
			EvictedCount: archiveStats.EvictedCount,
		}
	}
	return summary, nil
}

// RunArchiveMaintenance evicts over-quota mail bodies in batches, removes stale
// orphan temporary files in .tmp, and purges expired admin sessions.
func (s *Store) RunArchiveMaintenance(ctx context.Context) (ArchiveStats, int, int64, error) {
	if err := s.EnforceMailArchiveLimit(ctx); err != nil {
		return ArchiveStats{}, 0, 0, err
	}
	cleanedTemp := s.cleanStaleArchiveTempFiles(30 * time.Minute)
	cleanedSessions, _ := s.DeleteExpiredSessions(ctx)
	stats, err := s.MailArchiveStats(ctx)
	return stats, cleanedTemp, cleanedSessions, err
}

func (s *Store) cleanStaleArchiveTempFiles(maxAge time.Duration) int {
	tempDir := s.MailArchiveTempDir()
	if tempDir == "" {
		return 0
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return 0
	}
	cutoff := s.now().Add(-maxAge)
	cleaned := 0
	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), archiveSourceResetPrefix) {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(tempDir, entry.Name())) == nil {
			cleaned++
		}
	}
	return cleaned
}
