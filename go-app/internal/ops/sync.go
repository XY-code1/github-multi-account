// 后台同步引擎：定时同步所有账号，失败隔离 + 指数退避重试。
//
// 设计要点（面向几百个账号）：
//   - 并发限制（默认 5，可配置），避免瞬间打满带宽；
//   - 单账号失败不影响其他账号；
//   - 连续失败后按指数退避推迟重试（60s → 120s → 240s，上限 1 小时，带抖动）；
//   - 同步结果写入 summaries 表，仪表盘读缓存，不实时请求 GitHub；
//   - 记录 API 配额与同步状态，供「需要关注」判断使用。
package ops

import (
	"database/sql"
	"encoding/json"
	"log"
	"math/rand"
	"sync"
	"time"

	"github-multi-account/internal/config"
	"github-multi-account/internal/service"
)

// Config 是同步引擎配置（对应 config/settings.yaml 的 sync 段）。
type Config struct {
	Enabled            bool `json:"enabled" yaml:"enabled"`
	IntervalSeconds    int  `json:"interval_seconds" yaml:"interval_seconds"`
	Concurrency        int  `json:"concurrency" yaml:"concurrency"`
	MaxRetries         int  `json:"max_retries" yaml:"max_retries"`
	BaseBackoffSeconds int  `json:"base_backoff_seconds" yaml:"base_backoff_seconds"`
	MaxBackoffSeconds  int  `json:"max_backoff_seconds" yaml:"max_backoff_seconds"`
	// 单账号剩余配额低于该值时本轮跳过，避免把配额打满
	QuotaFloor int `json:"quota_floor" yaml:"quota_floor"`
}

// DefaultConfig 是默认配置。
func DefaultConfig() Config {
	return Config{
		Enabled:            true,
		IntervalSeconds:    900,
		Concurrency:        5,
		MaxRetries:         3,
		BaseBackoffSeconds: 60,
		MaxBackoffSeconds:  3600,
		QuotaFloor:         100,
	}
}

// Progress 是一轮同步的进度。
type Progress struct {
	Status          string  `json:"status"`
	Total           int     `json:"total"`
	Done            int     `json:"done"`
	Failed          int     `json:"failed"`
	StartedAt       string  `json:"started_at"`
	FinishedAt      string  `json:"finished_at"`
	Current         string  `json:"current"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// Result 是一轮同步的结果统计。
type Result struct {
	Total  int `json:"total"`
	Done   int `json:"done"`
	Failed int `json:"failed"`
}

// SettingsUpdate 是同步配置的部分更新（nil 表示不修改）。
type SettingsUpdate struct {
	Enabled            *bool `json:"enabled"`
	IntervalSeconds    *int  `json:"interval_seconds"`
	Concurrency        *int  `json:"concurrency"`
	MaxRetries         *int  `json:"max_retries"`
	BaseBackoffSeconds *int  `json:"base_backoff_seconds"`
	MaxBackoffSeconds  *int  `json:"max_backoff_seconds"`
}

// Engine 是后台同步引擎。
type Engine struct {
	db   *sql.DB
	cfg  *config.Store
	svc  *service.Service
	dir  string
	mu   sync.Mutex
	conf Config
	prog Progress
	stop chan struct{}
	once sync.Once
	loop bool
}

// NewEngine 创建引擎并加载已保存的配置。
func NewEngine(db *sql.DB, cfg *config.Store, svc *service.Service, configDir string) *Engine {
	e := &Engine{
		db:   db,
		cfg:  cfg,
		svc:  svc,
		dir:  configDir,
		conf: LoadSettings(configDir),
		prog: Progress{Status: "idle"},
	}
	return e
}

// LoadSettings 读取配置，缺失或损坏时回退默认值。
// 只有显式出现在文件中的键才会覆盖默认值（与 Python 版一致）。
func LoadSettings(dir string) Config {
	cfg := DefaultConfig()
	raw, ok := config.ReadSettings(dir)["sync"].(map[string]any)
	if !ok {
		return cfg
	}
	if v, ok := raw["enabled"].(bool); ok {
		cfg.Enabled = v
	}
	if v, ok := asInt(raw["interval_seconds"]); ok && v != 0 {
		cfg.IntervalSeconds = v
	}
	if v, ok := asInt(raw["concurrency"]); ok && v != 0 {
		cfg.Concurrency = v
	}
	if v, ok := asInt(raw["max_retries"]); ok && v != 0 {
		cfg.MaxRetries = v
	}
	if v, ok := asInt(raw["base_backoff_seconds"]); ok && v != 0 {
		cfg.BaseBackoffSeconds = v
	}
	if v, ok := asInt(raw["max_backoff_seconds"]); ok && v != 0 {
		cfg.MaxBackoffSeconds = v
	}
	if v, ok := asInt(raw["quota_floor"]); ok && v != 0 {
		cfg.QuotaFloor = v
	}
	return cfg
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

// SaveSettings 持久化配置（保留通知渠道等其他分节）。
func SaveSettings(dir string, cfg Config) error {
	data := config.ReadSettings(dir)
	data["sync"] = cfg
	return config.WriteSettings(dir, data)
}

// Configure 应用部分更新并持久化。
func (e *Engine) Configure(u SettingsUpdate) Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	c := e.conf
	if u.Enabled != nil {
		c.Enabled = *u.Enabled
	}
	if u.IntervalSeconds != nil {
		c.IntervalSeconds = *u.IntervalSeconds
	}
	if u.Concurrency != nil {
		c.Concurrency = *u.Concurrency
	}
	if u.MaxRetries != nil {
		c.MaxRetries = *u.MaxRetries
	}
	if u.BaseBackoffSeconds != nil {
		c.BaseBackoffSeconds = *u.BaseBackoffSeconds
	}
	if u.MaxBackoffSeconds != nil {
		c.MaxBackoffSeconds = *u.MaxBackoffSeconds
	}
	e.conf = c
	if err := SaveSettings(e.dir, c); err != nil {
		log.Printf("保存同步配置失败: %v", err)
	}
	return c
}

// Config 返回当前配置副本。
func (e *Engine) Config() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.conf
}

// Progress 返回当前进度副本。
func (e *Engine) Progress() Progress {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.prog
}

// IsRunning 返回后台循环是否在运行。
func (e *Engine) IsRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loop
}

// Start 启动后台循环（幂等）。
func (e *Engine) Start() {
	e.mu.Lock()
	if e.loop {
		e.mu.Unlock()
		return
	}
	e.loop = true
	e.stop = make(chan struct{})
	e.mu.Unlock()
	go e.loopRun()
}

// Stop 停止后台循环。
func (e *Engine) Stop() {
	e.mu.Lock()
	if !e.loop {
		e.mu.Unlock()
		return
	}
	e.loop = false
	stop := e.stop
	e.mu.Unlock()
	close(stop)
}

func (e *Engine) loopRun() {
	base := e.Config().IntervalSeconds
	interval := base

	e.mu.Lock()
	stop := e.stop
	e.mu.Unlock()

	for {
		if interval < 0 {
			interval = 0
		}
		select {
		case <-stop:
			return
		case <-time.After(time.Duration(interval) * time.Second):
		}

		e.mu.Lock()
		enabled := e.conf.Enabled
		e.mu.Unlock()
		if !enabled {
			continue
		}

		started := time.Now()
		e.SyncAll(nil)
		elapsed := time.Since(started)

		e.mu.Lock()
		e.prog.DurationSeconds = round1(elapsed.Seconds())
		e.mu.Unlock()

		// 自适应间隔：本轮耗时超过间隔时延长下一轮，避免积压。
		// 只影响本地循环变量，不回写用户配置。
		if base > 0 && elapsed > time.Duration(interval)*time.Second {
			next := int(elapsed.Seconds()*1.2) + 60
			if cap := maxInt(base*3, 3600); next > cap {
				next = cap
			}
			log.Printf("同步耗时 %.0fs 超过间隔，下一轮间隔调整为 %ds", elapsed.Seconds(), next)
			interval = next
		} else {
			interval = base
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}

// SyncAll 同步全部账号（或指定 ID）。并发受限，单账号失败被隔离。
func (e *Engine) SyncAll(onlyIDs []int) Result {
	accounts, err := e.cfg.Accounts()
	if err != nil {
		log.Printf("读取账号失败: %v", err)
		return Result{}
	}
	if len(onlyIDs) > 0 {
		want := make(map[int]bool, len(onlyIDs))
		for _, id := range onlyIDs {
			want[id] = true
		}
		filtered := accounts[:0]
		for _, a := range accounts {
			if want[a.ID] {
				filtered = append(filtered, a)
			}
		}
		accounts = filtered
	}
	if len(accounts) == 0 {
		return Result{}
	}

	e.mu.Lock()
	e.prog = Progress{
		Status:    "running",
		Total:     len(accounts),
		StartedAt: time.Now().UTC().Format(time.RFC3339),
	}
	e.mu.Unlock()

	cfg := e.Config()
	sem := make(chan struct{}, max1(cfg.Concurrency))
	var wg sync.WaitGroup
	var mu sync.Mutex
	done, failed := 0, 0

	for _, a := range accounts {
		wg.Add(1)
		go func(acc config.Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			e.mu.Lock()
			e.prog.Current = acc.Username
			e.mu.Unlock()

			ok, _, _ := e.SyncAccount(acc)

			mu.Lock()
			if ok {
				done++
			} else {
				failed++
			}
			e.mu.Lock()
			e.prog.Done = done
			e.prog.Failed = failed
			e.mu.Unlock()
			mu.Unlock()
		}(a)
	}
	wg.Wait()

	e.mu.Lock()
	e.prog.Status = "idle"
	e.prog.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	e.prog.Current = ""
	e.mu.Unlock()

	return Result{Total: len(accounts), Done: done, Failed: failed}
}

// SyncAccount 同步单个账号：失败隔离 + 指数退避。返回是否成功。
func (e *Engine) SyncAccount(acc config.Account) (bool, string, string) {
	aid := acc.ID
	state, _ := GetSyncState(e.db, aid)
	if state != nil && !state.Enabled() {
		return false, "skipped", "sync_disabled"
	}
	limits, _ := AllRateLimits(e.db)
	if rl, ok := limits[aid]; ok && rl.Remaining < e.Config().QuotaFloor {
		return false, "skipped", "quota_low"
	}
	if state != nil && state.NextRetryAt != "" {
		if t, err := time.Parse(time.RFC3339, state.NextRetryAt); err == nil && time.Now().UTC().Before(t) {
			return false, "deferred", ""
		}
	}

	// 增量同步：带上各端点的 ETag，未变化（304）不消耗 API 配额
	etags := map[string]string{}
	for _, key := range []string{"review_prs", "my_prs", "issues", "events"} {
		if v, err := GetETag(e.db, aid, key); err == nil && v != "" {
			etags[key] = v
		}
	}
	var previous *service.Status
	if row, err := GetSummary(e.db, aid); err == nil && row != nil && len(row.Payload) > 0 {
		var p service.Status
		if json.Unmarshal(row.Payload, &p) == nil {
			previous = &p
		}
	}
	st := e.svc.SyncSummaryCond(acc, etags, previous)
	for key, value := range etags {
		_ = PutETag(e.db, aid, key, value)
	}
	ts := time.Now().UTC().Format(time.RFC3339)

	UpsertSyncState(e.db, SyncState{
		AccountID:   aid,
		Username:    acc.Username,
		LastSyncAt:  ts,
		LastStatus:  st.Status,
		LastError:   st.Error,
		SyncEnabled: 1,
		UpdatedAt:   ts,
	}, &Meta{
		AccountID: aid,
		Username:  acc.Username,
		Type:      st.Type,
		Tags:      st.Tags,
		Note:      acc.Note,
		Alias:     acc.Alias,
		GroupName: st.GroupName,
		UpdatedAt: ts,
	})

	if st.Status == "active" {
		_ = ClearSyncFailure(e.db, aid)
	} else {
		failures := 0
		if state != nil {
			failures = state.ConsecutiveFailures
		}
		failures++
		backoff := e.Config().BaseBackoffSeconds
		for i := 1; i < failures && backoff < e.Config().MaxBackoffSeconds; i++ {
			backoff *= 2
		}
		if max := e.Config().MaxBackoffSeconds; backoff > max {
			backoff = max
		}
		backoff = int(float64(backoff) * (0.5 + rand.Float64()))
		next := time.Now().UTC().Add(time.Duration(backoff) * time.Second).Format(time.RFC3339)
		_ = UpsertSyncState(e.db, SyncState{
			AccountID:           aid,
			Username:            acc.Username,
			ConsecutiveFailures: failures,
			NextRetryAt:         next,
			SyncEnabled:         1,
			UpdatedAt:           ts,
		}, nil)
	}

	if st.RateLimit != nil {
		if resources, ok := st.RateLimit["resources"].(map[string]any); ok {
			if core, ok := resources["core"].(map[string]any); ok {
				_ = UpsertRateLimit(e.db, RateLimit{
					AccountID:  aid,
					Username:   acc.Username,
					Remaining:  toInt(core["remaining"]),
					QuotaLimit: toInt(core["limit"]),
					UpdatedAt:  ts,
				})
			}
		}
	}

	payload, _ := json.Marshal(st)
	_ = UpsertSummary(e.db, SummaryRow{
		AccountID:      aid,
		Username:       acc.Username,
		Status:         st.Status,
		Label:          st.Label,
		Type:           st.Type,
		GroupName:      st.GroupName,
		Tags:           st.Tags,
		Followers:      st.FollowersCount,
		Following:      st.FollowingCount,
		PublicRepos:    st.PublicRepos,
		ReviewPRs:      st.ReviewPRsCount,
		MyPRs:          st.MyPRsCount,
		AssignedIssues: st.AssignedIssuesCount,
		TotalStars:     st.TotalStars,
		Payload:        payload,
		Error:          st.Error,
		SyncedAt:       ts,
	})
	return st.Status == "active", st.Status, ""
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	default:
		return 0
	}
}

func max1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}
