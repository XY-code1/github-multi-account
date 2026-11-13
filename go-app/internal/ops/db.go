// Package ops 提供运维中心的数据层：同步状态、API 配额、审计日志、
// 账号元数据与汇总缓存。所有表与 Python 版共用同一个 data/dashboard.db，
// 主键列名保持 account_id 以兼容两套实现。
package ops

import (
	"database/sql"
	"encoding/json"
	"sync"
	"time"

	"github-multi-account/internal/github"
)

// Schema 是运维中心的表结构（幂等）。
const Schema = `
CREATE TABLE IF NOT EXISTS sync_state (
	account_id INTEGER PRIMARY KEY,
	username TEXT NOT NULL,
	last_sync_at TEXT,
	last_status TEXT DEFAULT 'unknown',
	last_error TEXT,
	consecutive_failures INTEGER DEFAULT 0,
	next_retry_at TEXT,
	sync_enabled INTEGER DEFAULT 1,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sync_status ON sync_state(last_status);
CREATE INDEX IF NOT EXISTS idx_sync_enabled ON sync_state(sync_enabled);

CREATE TABLE IF NOT EXISTS rate_limits (
	account_id INTEGER PRIMARY KEY,
	username TEXT NOT NULL,
	remaining INTEGER DEFAULT 0,
	quota_limit INTEGER DEFAULT 0,
	reset_at TEXT,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	action TEXT NOT NULL,
	account_id INTEGER,
	username TEXT,
	detail TEXT
);
CREATE INDEX IF NOT EXISTS idx_audit_ts ON audit_log(ts);
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_log(action);

CREATE TABLE IF NOT EXISTS account_meta (
	account_id INTEGER PRIMARY KEY,
	username TEXT NOT NULL,
	type TEXT DEFAULT 'personal',
	tags TEXT DEFAULT '[]',
	note TEXT DEFAULT '',
	alias TEXT DEFAULT '',
	group_name TEXT DEFAULT '',
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_meta_username ON account_meta(username);
CREATE INDEX IF NOT EXISTS idx_meta_group ON account_meta(group_name);

CREATE TABLE IF NOT EXISTS summaries (
	account_id INTEGER PRIMARY KEY,
	username TEXT NOT NULL,
	status TEXT DEFAULT 'unknown',
	label TEXT,
	type TEXT DEFAULT 'personal',
	group_name TEXT DEFAULT '',
	tags TEXT DEFAULT '[]',
	followers INTEGER DEFAULT 0,
	following INTEGER DEFAULT 0,
	public_repos INTEGER DEFAULT 0,
	review_prs INTEGER DEFAULT 0,
	my_prs INTEGER DEFAULT 0,
	assigned_issues INTEGER DEFAULT 0,
	total_stars INTEGER DEFAULT 0,
	payload TEXT,
	error TEXT,
	synced_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_summaries_status ON summaries(status);
CREATE INDEX IF NOT EXISTS idx_summaries_username ON summaries(username);
CREATE INDEX IF NOT EXISTS idx_summaries_group ON summaries(group_name);

CREATE TABLE IF NOT EXISTS api_cache (
	account_id INTEGER NOT NULL,
	endpoint TEXT NOT NULL,
	etag TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (account_id, endpoint)
);

CREATE TABLE IF NOT EXISTS social (
	account_id INTEGER NOT NULL,
	kind TEXT NOT NULL,
	login TEXT NOT NULL,
	avatar_url TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (account_id, kind, login)
);
CREATE INDEX IF NOT EXISTS idx_social_login ON social(login);

CREATE TABLE IF NOT EXISTS account_repo (
	account_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	stars INTEGER DEFAULT 0,
	language TEXT,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (account_id, name)
);

CREATE TABLE IF NOT EXISTS account_profile (
	account_id INTEGER PRIMARY KEY,
	cluster_index INTEGER,
	cluster_label TEXT,
	features TEXT,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS webhook_log (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	ts TEXT NOT NULL,
	url TEXT NOT NULL,
	event_type TEXT NOT NULL,
	ok INTEGER DEFAULT 0,
	detail TEXT
);
`

var (
	mu   sync.Mutex
	dbh  *sql.DB
)

// SetDB 设置全局数据库句柄（通常就是 store 打开的同一个库）。
func SetDB(db *sql.DB) {
	mu.Lock()
	defer mu.Unlock()
	dbh = db
}

// DB 返回当前数据库句柄。
func DB() *sql.DB {
	mu.Lock()
	defer mu.Unlock()
	return dbh
}

// Init 建表（幂等）。
func Init(db *sql.DB) error {
	mu.Lock()
	defer mu.Unlock()
	if _, err := db.Exec(Schema); err != nil {
		return err
	}
	dbh = db
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func ns(v sql.NullString) string { return v.String }

// nullStr 把空字符串转成 NULL，供 COALESCE 判断「是否提供该字段」。
func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func ni(v sql.NullInt64) int {
	if !v.Valid {
		return 0
	}
	return int(v.Int64)
}

// ---------------- 同步状态 ----------------

// SyncState 是一行同步状态。SyncEnabled 与 Python 一致，用 1/0 表示。
type SyncState struct {
	AccountID           int    `json:"account_id"`
	Username            string `json:"username"`
	LastSyncAt          string `json:"last_sync_at"`
	LastStatus          string `json:"last_status"`
	LastError           string `json:"last_error"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	NextRetryAt         string `json:"next_retry_at"`
	SyncEnabled         int    `json:"sync_enabled"`
	UpdatedAt           string `json:"updated_at"`
}

// Enabled 返回是否启用同步。
func (s SyncState) Enabled() bool { return s.SyncEnabled != 0 }

// Meta 是一行账号元数据（搜索 / 筛选镜像）。
type Meta struct {
	AccountID int      `json:"account_id"`
	Username  string   `json:"username"`
	Type      string   `json:"type"`
	Tags      []string `json:"tags"`
	Note      string   `json:"note"`
	Alias     string   `json:"alias"`
	GroupName string   `json:"group_name"`
	UpdatedAt string   `json:"updated_at"`
}

// UpsertSyncState 写入同步状态。meta 非空时同步更新 account_meta。
func UpsertSyncState(db *sql.DB, s SyncState, meta *Meta) error {
	mu.Lock()
	defer mu.Unlock()
	enabled := 0
	if s.Enabled() {
		enabled = 1
	}
	// 空字符串按 NULL 处理，配合 COALESCE 实现「未提供则保留旧值」，
	// 与 Python 版 upsert_sync_state 的语义一致。
	_, err := db.Exec(`
		INSERT INTO sync_state (account_id, username, last_sync_at, last_status, last_error,
			consecutive_failures, next_retry_at, sync_enabled, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(account_id) DO UPDATE SET
			username=excluded.username,
			last_sync_at=COALESCE(excluded.last_sync_at, last_sync_at),
			last_status=COALESCE(excluded.last_status, last_status),
			last_error=excluded.last_error,
			consecutive_failures=COALESCE(excluded.consecutive_failures, 0),
			next_retry_at=excluded.next_retry_at,
			sync_enabled=COALESCE(excluded.sync_enabled, sync_enabled),
			updated_at=excluded.updated_at
	`, s.AccountID, s.Username, nullStr(s.LastSyncAt), nullStr(s.LastStatus), nullStr(s.LastError),
		s.ConsecutiveFailures, nullStr(s.NextRetryAt), enabled, s.UpdatedAt)
	if err != nil {
		return err
	}
	if meta != nil {
		return upsertMetaLocked(db, *meta)
	}
	return nil
}

func upsertMetaLocked(db *sql.DB, m Meta) error {
	tags, _ := json.Marshal(nonNilTags(m.Tags))
	if m.Type == "" {
		m.Type = "personal"
	}
	_, err := db.Exec(`
		INSERT INTO account_meta (account_id, username, type, tags, note, alias, group_name, updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(account_id) DO UPDATE SET
			username=excluded.username, type=excluded.type, tags=excluded.tags,
			note=excluded.note, alias=excluded.alias, group_name=excluded.group_name,
			updated_at=excluded.updated_at
	`, m.AccountID, m.Username, m.Type, string(tags), m.Note, m.Alias, m.GroupName, m.UpdatedAt)
	return err
}

// SetSyncEnabled 启用 / 暂停某账号的同步。
// 账号从未同步过（sync_state 里没有行）时也会创建状态行，否则暂停操作会静默失效。
func SetSyncEnabled(db *sql.DB, id int, username string, enabled bool) error {
	mu.Lock()
	defer mu.Unlock()
	v := 0
	if enabled {
		v = 1
	}
	_, err := db.Exec(`
		INSERT INTO sync_state (account_id, username, sync_enabled, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(account_id) DO UPDATE SET sync_enabled=excluded.sync_enabled, updated_at=excluded.updated_at
	`, id, username, v, now())
	return err
}

// ClearSyncFailure 清除连续失败计数。
func ClearSyncFailure(db *sql.DB, id int) error {
	mu.Lock()
	defer mu.Unlock()
	_, err := db.Exec(`UPDATE sync_state SET consecutive_failures=0, next_retry_at=NULL,
		updated_at=? WHERE account_id=?`, now(), id)
	return err
}

// GetSyncState 返回单行同步状态，不存在时返回 nil。
func GetSyncState(db *sql.DB, id int) (*SyncState, error) {
	mu.Lock()
	defer mu.Unlock()
	var (
		s       SyncState
		lastAt  sql.NullString
		status  sql.NullString
		errMsg  sql.NullString
		nextAt  sql.NullString
		enabled sql.NullInt64
		updated sql.NullString
	)
	err := db.QueryRow(`SELECT account_id, username, last_sync_at, last_status, last_error,
		consecutive_failures, next_retry_at, sync_enabled, updated_at FROM sync_state WHERE account_id=?`, id).
		Scan(&s.AccountID, &s.Username, &lastAt, &status, &errMsg,
			&s.ConsecutiveFailures, &nextAt, &enabled, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.LastSyncAt, s.LastStatus, s.LastError, s.NextRetryAt = lastAt.String, status.String, errMsg.String, nextAt.String
	s.SyncEnabled, s.UpdatedAt = int(enabled.Int64), updated.String
	return &s, nil
}

// AllSyncStates 返回全部同步状态。
func AllSyncStates(db *sql.DB) ([]SyncState, error) {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT account_id, username, last_sync_at, last_status, last_error,
		consecutive_failures, next_retry_at, sync_enabled, updated_at FROM sync_state ORDER BY account_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []SyncState{}
	for rows.Next() {
		var (
			s       SyncState
			lastAt  sql.NullString
			status  sql.NullString
			errMsg  sql.NullString
			nextAt  sql.NullString
			enabled sql.NullInt64
			updated sql.NullString
		)
		if err := rows.Scan(&s.AccountID, &s.Username, &lastAt, &status, &errMsg,
			&s.ConsecutiveFailures, &nextAt, &enabled, &updated); err != nil {
			return nil, err
		}
		s.LastSyncAt, s.LastStatus, s.LastError, s.NextRetryAt = lastAt.String, status.String, errMsg.String, nextAt.String
		s.SyncEnabled, s.UpdatedAt = int(enabled.Int64), updated.String
		out = append(out, s)
	}
	return out, rows.Err()
}

// ---------------- API 配额 ----------------

// RateLimit 是一行 API 配额。
type RateLimit struct {
	AccountID  int    `json:"account_id"`
	Username   string `json:"username"`
	Remaining  int    `json:"remaining"`
	QuotaLimit int    `json:"quota_limit"`
	ResetAt    string `json:"reset_at"`
	UpdatedAt  string `json:"updated_at"`
}

// UpsertRateLimit 写入配额。
func UpsertRateLimit(db *sql.DB, r RateLimit) error {
	mu.Lock()
	defer mu.Unlock()
	_, err := db.Exec(`
		INSERT INTO rate_limits (account_id, username, remaining, quota_limit, reset_at, updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(account_id) DO UPDATE SET
			username=excluded.username, remaining=excluded.remaining,
			quota_limit=excluded.quota_limit, reset_at=excluded.reset_at, updated_at=excluded.updated_at
	`, r.AccountID, r.Username, r.Remaining, r.QuotaLimit, r.ResetAt, r.UpdatedAt)
	return err
}

// AllRateLimits 返回全部配额，按账号 ID 索引。
func AllRateLimits(db *sql.DB) (map[int]RateLimit, error) {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT account_id, username, remaining, quota_limit, reset_at, updated_at FROM rate_limits`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[int]RateLimit{}
	for rows.Next() {
		var (
			r       RateLimit
			resetAt sql.NullString
			updated sql.NullString
		)
		if err := rows.Scan(&r.AccountID, &r.Username, &r.Remaining, &r.QuotaLimit, &resetAt, &updated); err != nil {
			return nil, err
		}
		r.ResetAt, r.UpdatedAt = resetAt.String, updated.String
		out[r.AccountID] = r
	}
	return out, rows.Err()
}

// ---------------- 审计日志 ----------------

// AuditItem 是一条审计记录。
type AuditItem struct {
	TS        string `json:"ts"`
	Action    string `json:"action"`
	AccountID *int   `json:"account_id"`
	Username  string `json:"username"`
	Detail    string `json:"detail"`
}

// Audit 记录一条审计日志。detail 必须不含敏感信息。
func Audit(db *sql.DB, action string, accountID *int, username, detail string) {
	if db == nil {
		return
	}
	if len(detail) > 500 {
		detail = detail[:500]
	}
	mu.Lock()
	defer mu.Unlock()
	_, _ = db.Exec(`INSERT INTO audit_log (ts, action, account_id, username, detail) VALUES (?,?,?,?,?)`,
		now(), action, accountID, username, detail)
}

// PruneAudit 清理审计日志：只保留最近 limit 条，且不超过 maxDays 天。
// 审计日志只增不减，长期运行会无限膨胀，启动时调用一次即可。
func PruneAudit(db *sql.DB, limit, maxDays int) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	cutoff := time.Now().UTC().AddDate(0, 0, -maxDays).Format(time.RFC3339)
	total := 0
	if res, err := db.Exec(`DELETE FROM audit_log WHERE ts < ?`, cutoff); err == nil {
		n, _ := res.RowsAffected()
		total += int(n)
	}
	if res, err := db.Exec(`DELETE FROM audit_log WHERE id NOT IN (
		SELECT id FROM audit_log ORDER BY id DESC LIMIT ?
	)`, limit); err == nil {
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}

// ListAudit 返回审计日志（新的在前）。
func ListAudit(db *sql.DB, limit int, action string) ([]AuditItem, error) {
	mu.Lock()
	defer mu.Unlock()
	q := `SELECT ts, action, account_id, username, detail FROM audit_log`
	var args []any
	if action != "" {
		q += ` WHERE action=?`
		args = append(args, action)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []AuditItem{}
	for rows.Next() {
		var (
			a     AuditItem
			uname sql.NullString
			det   sql.NullString
		)
		if err := rows.Scan(&a.TS, &a.Action, &a.AccountID, &uname, &det); err != nil {
			return nil, err
		}
		a.Username, a.Detail = uname.String, det.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------- 出站 Webhook 日志 ----------------

// WebhookLogItem 是一条 Webhook 投递记录。
type WebhookLogItem struct {
	TS        string `json:"ts"`
	URL       string `json:"url"`
	EventType string `json:"event_type"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail"`
}

// LogWebhook 记录一次 Webhook 投递。
func LogWebhook(db *sql.DB, url, eventType string, ok bool, detail string) {
	mu.Lock()
	defer mu.Unlock()
	v := 0
	if ok {
		v = 1
	}
	_, _ = db.Exec(`INSERT INTO webhook_log (ts, url, event_type, ok, detail) VALUES (?,?,?,?,?)`,
		now(), url, eventType, v, detail)
}

// ListWebhookLog 返回 Webhook 投递日志（新的在前）。
func ListWebhookLog(db *sql.DB, limit int) ([]WebhookLogItem, error) {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT ts, url, event_type, ok, detail FROM webhook_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []WebhookLogItem{}
	for rows.Next() {
		var (
			item WebhookLogItem
			ok   int
			det  sql.NullString
		)
		if err := rows.Scan(&item.TS, &item.URL, &item.EventType, &ok, &det); err != nil {
			return nil, err
		}
		item.OK = ok == 1
		item.Detail = det.String
		out = append(out, item)
	}
	return out, rows.Err()
}

// ---------------- 关系图谱数据 ----------------

// SocialRow 是一条社交关系。
type SocialRow struct {
	AccountID int    `json:"account_id"`
	Kind      string `json:"kind"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

// RepoRow 是一个账号的代表仓库。
type RepoRow struct {
	AccountID int    `json:"account_id"`
	Name      string `json:"name"`
	Stars     int    `json:"stars"`
	Language  string `json:"language"`
}

// ReplaceSocial 覆盖某账号某类型的社交关系。
func ReplaceSocial(db *sql.DB, accountID int, kind string, entries []github.Person) error {
	mu.Lock()
	defer mu.Unlock()
	if _, err := db.Exec(`DELETE FROM social WHERE account_id=? AND kind=?`, accountID, kind); err != nil {
		return err
	}
	ts := now()
	for _, e := range entries {
		if _, err := db.Exec(`INSERT INTO social (account_id, kind, login, avatar_url, updated_at) VALUES (?,?,?,?,?)`,
			accountID, kind, e.Login, e.AvatarURL, ts); err != nil {
			return err
		}
	}
	return nil
}

// ReplaceRepos 覆盖某账号的代表仓库。
func ReplaceRepos(db *sql.DB, accountID int, repos []github.Repo) error {
	mu.Lock()
	defer mu.Unlock()
	if _, err := db.Exec(`DELETE FROM account_repo WHERE account_id=?`, accountID); err != nil {
		return err
	}
	ts := now()
	for _, r := range repos {
		if _, err := db.Exec(`INSERT INTO account_repo (account_id, name, stars, language, updated_at) VALUES (?,?,?,?,?)`,
			accountID, r.Name, r.StargazersCount, r.Language, ts); err != nil {
			return err
		}
	}
	return nil
}

// CountSocial 返回社交关系记录数（用于判断是否需要首次同步）。
func CountSocial(db *sql.DB) (int, error) {
	mu.Lock()
	defer mu.Unlock()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM social`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// AllSocial 返回全部社交关系。
func AllSocial(db *sql.DB) ([]SocialRow, error) {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT account_id, kind, login, avatar_url FROM social`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []SocialRow{}
	for rows.Next() {
		var r SocialRow
		if err := rows.Scan(&r.AccountID, &r.Kind, &r.Login, &r.AvatarURL); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AllRepos 返回全部账号仓库。
func AllRepos(db *sql.DB) ([]RepoRow, error) {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT account_id, name, stars, language FROM account_repo`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []RepoRow{}
	for rows.Next() {
		var r RepoRow
		if err := rows.Scan(&r.AccountID, &r.Name, &r.Stars, &r.Language); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---------------- 增量同步 ETag ----------------

// GetETag 返回某账号某端点缓存的 ETag（无则空字符串）。
func GetETag(db *sql.DB, accountID int, endpoint string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	var etag sql.NullString
	err := db.QueryRow(`SELECT etag FROM api_cache WHERE account_id=? AND endpoint=?`, accountID, endpoint).
		Scan(&etag)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return etag.String, nil
}

// PutETag 写入某账号某端点的 ETag。
func PutETag(db *sql.DB, accountID int, endpoint, etag string) error {
	mu.Lock()
	defer mu.Unlock()
	_, err := db.Exec(`
		INSERT INTO api_cache (account_id, endpoint, etag, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(account_id, endpoint) DO UPDATE SET etag=excluded.etag, updated_at=excluded.updated_at
	`, accountID, endpoint, nullStr(etag), now())
	return err
}

// ClearETags 清除某账号的 ETag 缓存（删除账号或 Token 变更时）。
func ClearETags(db *sql.DB, accountID int) error {
	mu.Lock()
	defer mu.Unlock()
	_, err := db.Exec(`DELETE FROM api_cache WHERE account_id=?`, accountID)
	return err
}

// ---------------- 账号元数据 ----------------

// RebuildMeta 从账号配置重建元数据镜像。
func RebuildMeta(db *sql.DB, metas []Meta) error {
	mu.Lock()
	defer mu.Unlock()
	if _, err := db.Exec(`DELETE FROM account_meta`); err != nil {
		return err
	}
	for _, m := range metas {
		m.UpdatedAt = now()
		if err := upsertMetaLocked(db, m); err != nil {
			return err
		}
	}
	return nil
}

// AllMeta 返回全部账号元数据。
func AllMeta(db *sql.DB) ([]Meta, error) {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT account_id, username, type, tags, note, alias, group_name, updated_at
		FROM account_meta ORDER BY account_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Meta{}
	for rows.Next() {
		var (
			m    Meta
			tags string
		)
		if err := rows.Scan(&m.AccountID, &m.Username, &m.Type, &tags, &m.Note, &m.Alias, &m.GroupName, &m.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(tags), &m.Tags)
		m.Tags = nonNilTags(m.Tags)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ---------------- 汇总缓存 ----------------

// SummaryRow 是一行汇总缓存。Payload 保存完整的汇总 JSON（含 events）。
type SummaryRow struct {
	AccountID      int               `json:"account_id"`
	Username       string            `json:"username"`
	Status         string            `json:"status"`
	Label          string            `json:"label"`
	Type           string            `json:"type"`
	GroupName      string            `json:"group_name"`
	Tags           []string          `json:"tags"`
	Followers      int               `json:"followers"`
	Following      int               `json:"following"`
	PublicRepos    int               `json:"public_repos"`
	ReviewPRs      int               `json:"review_prs"`
	MyPRs          int               `json:"my_prs"`
	AssignedIssues int               `json:"assigned_issues"`
	TotalStars     int               `json:"total_stars"`
	Payload        json.RawMessage   `json:"payload"`
	Error          string            `json:"error"`
	SyncedAt       string            `json:"synced_at"`
}

// UpsertSummary 写入汇总缓存。
func UpsertSummary(db *sql.DB, s SummaryRow) error {
	mu.Lock()
	defer mu.Unlock()
	if s.Type == "" {
		s.Type = "personal"
	}
	tags, _ := json.Marshal(nonNilTags(s.Tags))
	if s.Payload == nil {
		s.Payload = json.RawMessage("{}")
	}
	_, err := db.Exec(`
		INSERT INTO summaries (account_id, username, status, label, type, group_name, tags,
			followers, following, public_repos, review_prs, my_prs, assigned_issues,
			total_stars, payload, error, synced_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(account_id) DO UPDATE SET
			username=excluded.username, status=excluded.status, label=excluded.label,
			type=excluded.type, group_name=excluded.group_name, tags=excluded.tags,
			followers=excluded.followers, following=excluded.following,
			public_repos=excluded.public_repos, review_prs=excluded.review_prs,
			my_prs=excluded.my_prs, assigned_issues=excluded.assigned_issues,
			total_stars=excluded.total_stars, payload=excluded.payload,
			error=excluded.error, synced_at=excluded.synced_at
	`, s.AccountID, s.Username, s.Status, s.Label, s.Type, s.GroupName, string(tags),
		s.Followers, s.Following, s.PublicRepos, s.ReviewPRs, s.MyPRs, s.AssignedIssues,
		s.TotalStars, string(s.Payload), s.Error, s.SyncedAt)
	return err
}

func scanSummaryRow(sc interface {
	Scan(dest ...any) error
}) (SummaryRow, error) {
	var (
		s       SummaryRow
		label   sql.NullString
		typ     sql.NullString
		group   sql.NullString
		tags    sql.NullString
		payload sql.NullString
		errMsg  sql.NullString
	)
	if err := sc.Scan(&s.AccountID, &s.Username, &s.Status, &label, &typ, &group, &tags,
		&s.Followers, &s.Following, &s.PublicRepos, &s.ReviewPRs, &s.MyPRs, &s.AssignedIssues,
		&s.TotalStars, &payload, &errMsg, &s.SyncedAt); err != nil {
		return s, err
	}
	s.Label, s.Type, s.GroupName, s.Error = label.String, typ.String, group.String, errMsg.String
	_ = json.Unmarshal([]byte(tags.String), &s.Tags)
	s.Tags = nonNilTags(s.Tags)
	s.Payload = json.RawMessage(payload.String)
	if len(s.Payload) == 0 {
		s.Payload = json.RawMessage("{}")
	}
	return s, nil
}

// AllSummaries 返回全部汇总缓存。
func AllSummaries(db *sql.DB) ([]SummaryRow, error) {
	mu.Lock()
	defer mu.Unlock()
	rows, err := db.Query(`SELECT account_id, username, status, label, type, group_name, tags,
		followers, following, public_repos, review_prs, my_prs, assigned_issues,
		total_stars, payload, error, synced_at FROM summaries ORDER BY account_id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []SummaryRow{}
	for rows.Next() {
		s, err := scanSummaryRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetSummary 返回单行汇总缓存，不存在时返回 nil。
func GetSummary(db *sql.DB, id int) (*SummaryRow, error) {
	mu.Lock()
	defer mu.Unlock()
	row := db.QueryRow(`SELECT account_id, username, status, label, type, group_name, tags,
		followers, following, public_repos, review_prs, my_prs, assigned_issues,
		total_stars, payload, error, synced_at FROM summaries WHERE account_id=?`, id)
	s, err := scanSummaryRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func nonNilTags(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}
