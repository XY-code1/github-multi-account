// Package store 用 SQLite 记录账号状态快照。
package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS snapshots (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	username TEXT NOT NULL,
	created_at TEXT NOT NULL,
	followers INTEGER DEFAULT 0,
	following INTEGER DEFAULT 0,
	total_stars INTEGER DEFAULT 0,
	public_repos INTEGER DEFAULT 0,
	review_prs INTEGER DEFAULT 0,
	my_prs INTEGER DEFAULT 0,
	assigned_issues INTEGER DEFAULT 0,
	events INTEGER DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_snapshots_user_time ON snapshots(username, created_at);
`

const snapshotInterval = 10 * time.Minute

// Store 是线程安全的快照存储。
type Store struct {
	db *sql.DB
	mu sync.Mutex
}

// Open 打开数据库并确保表结构存在。
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "dashboard.db"))
	if err != nil {
		return nil, err
	}
	// SQLite 单写者：串行化连接，避免后台同步与 HTTP 请求并发写时出现锁竞争。
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// DB 返回底层数据库句柄，供运维中心（ops）复用同一个库。
func (s *Store) DB() *sql.DB { return s.db }

// Metrics 是一次快照的指标。
type Metrics struct {
	Followers      int
	Following      int
	TotalStars     int
	PublicRepos    int
	ReviewPRs      int
	MyPRs          int
	AssignedIssues int
	Events         int
}

// SaveSnapshot 保存快照（默认 10 分钟节流，force 时忽略）。
func (s *Store) SaveSnapshot(username string, m Metrics, force bool) (bool, error) {
	if username == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC().Format(time.RFC3339)
	if !force {
		var last string
		if err := s.db.QueryRow(`SELECT created_at FROM snapshots WHERE username=? ORDER BY id DESC LIMIT 1`, username).Scan(&last); err == nil {
			if t, err := time.Parse(time.RFC3339, last); err == nil && time.Since(t) < snapshotInterval {
				return false, nil
			}
		}
	}
	_, err := s.db.Exec(
		`INSERT INTO snapshots (username, created_at, followers, following, total_stars, public_repos, review_prs, my_prs, assigned_issues, events)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		username, now, m.Followers, m.Following, m.TotalStars, m.PublicRepos, m.ReviewPRs, m.MyPRs, m.AssignedIssues, m.Events,
	)
	return err == nil, err
}

// Snapshot 是一条历史记录。
type Snapshot struct {
	CreatedAt      string `json:"created_at"`
	Followers      int    `json:"followers"`
	Following      int    `json:"following"`
	TotalStars     int    `json:"total_stars"`
	PublicRepos    int    `json:"public_repos"`
	ReviewPRs      int    `json:"review_prs"`
	MyPRs          int    `json:"my_prs"`
	AssignedIssues int    `json:"assigned_issues"`
	Events         int    `json:"events"`
}

// History 返回时间升序的历史。
func (s *Store) History(username string, limit int) ([]Snapshot, error) {
	out := []Snapshot{}
	if username == "" {
		return out, nil
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.Query(
		`SELECT created_at, followers, following, total_stars, public_repos, review_prs, my_prs, assigned_issues, events
		 FROM snapshots WHERE username=? ORDER BY created_at DESC LIMIT ?`, username, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sn Snapshot
		if err := rows.Scan(&sn.CreatedAt, &sn.Followers, &sn.Following, &sn.TotalStars, &sn.PublicRepos, &sn.ReviewPRs, &sn.MyPRs, &sn.AssignedIssues, &sn.Events); err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// Stats 返回快照统计。
type Stats struct {
	Count int    `json:"count"`
	First string `json:"first"`
	Last  string `json:"last"`
}

func (s *Store) Stats(username string) Stats {
	if username == "" {
		return Stats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var count int
	var first, last sql.NullString
	_ = s.db.QueryRow(`SELECT COUNT(*), MIN(created_at), MAX(created_at) FROM snapshots WHERE username=?`, username).
		Scan(&count, &first, &last)
	return Stats{Count: count, First: first.String, Last: last.String}
}

// DeleteHistory 删除某账号的全部历史。
func (s *Store) DeleteHistory(username string) (int, error) {
	if username == "" {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.Exec(`DELETE FROM snapshots WHERE username=?`, username)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Prune 清理过期与超量的快照。
func (s *Store) Prune(maxPerAccount, maxDays int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().UTC().AddDate(0, 0, -maxDays).Format(time.RFC3339)
	total := 0
	if res, err := s.db.Exec(`DELETE FROM snapshots WHERE created_at < ?`, cutoff); err == nil {
		n, _ := res.RowsAffected()
		total += int(n)
	}
	if res, err := s.db.Exec(
		`DELETE FROM snapshots WHERE id IN (
		   SELECT id FROM (
		     SELECT id, ROW_NUMBER() OVER (PARTITION BY username ORDER BY id DESC) AS rn
		     FROM snapshots
		   ) WHERE rn > ?
		 )`, maxPerAccount); err == nil {
		n, _ := res.RowsAffected()
		total += int(n)
	}
	return total, nil
}
