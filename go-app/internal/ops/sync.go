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
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"

	"github-multi-account/internal/config"
	"github-multi-account/internal/notify"
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
	// 反风控：单账号同步冷却（秒），该时间内重复同步直接返回 throttled
	MinSyncIntervalSeconds int `json:"min_sync_interval_seconds" yaml:"min_sync_interval_seconds"`
	// 反风控：同一轮内每个账号启动前随机延迟 0~该秒数（错峰）
	StaggerSeconds int `json:"stagger_seconds" yaml:"stagger_seconds"`
	// 自适应间隔上限（秒）
	MaxIntervalSeconds int `json:"max_interval_seconds" yaml:"max_interval_seconds"`
	// 是否启用按配额自适应间隔
	AdaptiveInterval bool `json:"adaptive_interval" yaml:"adaptive_interval"`
	// 本轮被跳过的账号占比达到该值时拉长下一轮间隔
	QuotaLowRatio float64 `json:"quota_low_ratio" yaml:"quota_low_ratio"`
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
		QuotaFloor:             100,
		MinSyncIntervalSeconds: 60,
		StaggerSeconds:         15,
		MaxIntervalSeconds:     3600,
		AdaptiveInterval:       true,
		QuotaLowRatio:          0.5,
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
	// Skipped 是一轮中没有真正发起同步的账号数（配额不足 / 退避 / 冷却）
	Skipped int `json:"skipped"`
}

// SettingsUpdate 是同步配置的部分更新（nil 表示不修改）。
type SettingsUpdate struct {
	Enabled                *bool    `json:"enabled"`
	IntervalSeconds        *int     `json:"interval_seconds"`
	Concurrency            *int     `json:"concurrency"`
	MaxRetries             *int     `json:"max_retries"`
	BaseBackoffSeconds     *int     `json:"base_backoff_seconds"`
	MaxBackoffSeconds      *int     `json:"max_backoff_seconds"`
	QuotaFloor             *int     `json:"quota_floor"`
	MinSyncIntervalSeconds *int     `json:"min_sync_interval_seconds"`
	StaggerSeconds         *int     `json:"stagger_seconds"`
	MaxIntervalSeconds     *int     `json:"max_interval_seconds"`
	AdaptiveInterval       *bool    `json:"adaptive_interval"`
	QuotaLowRatio          *float64 `json:"quota_low_ratio"`
}

// Engine 是后台同步引擎。
type Engine struct {
	db   *sql.DB
	cfg  *config.Store
	svc  *service.Service
	dir  string
	mu       sync.Mutex
	conf     Config
	prog     Progress
	stop     chan struct{}
	once     sync.Once
	loop     bool
	lastSync map[int]time.Time
}

// NewEngine 创建引擎并加载已保存的配置。
func NewEngine(db *sql.DB, cfg *config.Store, svc *service.Service, configDir string) *Engine {
	e := &Engine{
		db:   db,
		cfg:  cfg,
		svc:  svc,
		dir:  configDir,
		conf:     LoadSettings(configDir),
		prog:     Progress{Status: "idle"},
		lastSync: map[int]time.Time{},
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
	if v, ok := asInt(raw["min_sync_interval_seconds"]); ok && v > 0 {
		cfg.MinSyncIntervalSeconds = v
	}
	if v, ok := asInt(raw["stagger_seconds"]); ok && v > 0 {
		cfg.StaggerSeconds = v
	}
	if v, ok := asInt(raw["max_interval_seconds"]); ok && v > 0 {
		cfg.MaxIntervalSeconds = v
	}
	if v, ok := raw["adaptive_interval"].(bool); ok {
		cfg.AdaptiveInterval = v
	}
	if v, ok := asFloat(raw["quota_low_ratio"]); ok && v > 0 {
		cfg.QuotaLowRatio = v
	}
	return cfg
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
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
	if u.QuotaFloor != nil {
		c.QuotaFloor = *u.QuotaFloor
	}
	if u.MinSyncIntervalSeconds != nil {
		c.MinSyncIntervalSeconds = *u.MinSyncIntervalSeconds
	}
	if u.StaggerSeconds != nil {
		c.StaggerSeconds = *u.StaggerSeconds
	}
	if u.MaxIntervalSeconds != nil {
		c.MaxIntervalSeconds = *u.MaxIntervalSeconds
	}
	if u.AdaptiveInterval != nil {
		c.AdaptiveInterval = *u.AdaptiveInterval
	}
	if u.QuotaLowRatio != nil {
		c.QuotaLowRatio = *u.QuotaLowRatio
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
		result := e.SyncAll(nil)
		elapsed := time.Since(started)

		e.mu.Lock()
		e.prog.DurationSeconds = round1(elapsed.Seconds())
		e.mu.Unlock()

		// 自适应间隔：本轮多数账号因配额/冷却被跳过时拉长下一轮，
		// 避免在配额紧张期继续按固定频率空转；否则沿用原有的耗时保护逻辑。
		cfg := e.Config()
		newInterval := nextInterval(base, interval, result.Total, result.Skipped, elapsed, cfg)
		if newInterval != interval {
			log.Printf("下一轮同步间隔 %ds → %ds（本轮 %d/%d 个账号因配额/冷却跳过，耗时 %.0fs）",
				interval, newInterval, result.Skipped, result.Total, elapsed.Seconds())
		}
		interval = newInterval
	}
}

// nextInterval 计算下一轮同步间隔：配额紧张（跳过占比达标）优先拉长，
// 其次沿用"本轮耗时超过间隔"的保护逻辑，否则回到基础间隔。
func nextInterval(base, current, total, skipped int, elapsed time.Duration, cfg Config) int {
	maxInterval := cfg.MaxIntervalSeconds
	if maxInterval <= 0 {
		maxInterval = maxInt(base*4, 3600)
	}
	if cfg.AdaptiveInterval && total > 0 && cfg.QuotaLowRatio > 0 &&
		float64(skipped)/float64(total) >= cfg.QuotaLowRatio {
		return minInt(maxInt(current*2, base*2), maxInterval)
	}
	if base > 0 && elapsed > time.Duration(current)*time.Second {
		next := int(elapsed.Seconds()*1.2) + 60
		if cap := maxInt(base*3, 3600); next > cap {
			next = cap
		}
		return next
	}
	return base
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
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
	done, failed, skipped := 0, 0, 0

	for _, a := range accounts {
		wg.Add(1)
		go func(acc config.Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// 错峰：随机延迟后再发请求，避免同一时刻并发尖峰
			if cfg.StaggerSeconds > 0 {
				time.Sleep(time.Duration(rand.Float64() * float64(cfg.StaggerSeconds)) * time.Second)
			}

			e.mu.Lock()
			e.prog.Current = acc.Username
			e.mu.Unlock()

			ok, status, _ := e.SyncAccount(acc)

			mu.Lock()
			if ok {
				done++
				} else {
				failed++
			}
			if status == "skipped" || status == "deferred" || status == "throttled" {
				skipped++
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

	return Result{Total: len(accounts), Done: done, Failed: failed, Skipped: skipped}
}

// markSynced 记录账号本轮已发起同步（用于单账号冷却判定）。
func (e *Engine) markSynced(aid int) {
	e.mu.Lock()
	e.lastSync[aid] = time.Now()
	e.mu.Unlock()
}

// inCooldown 报告该账号是否处于同步冷却期（min_sync_interval_seconds 内不重复拉取）。
func (e *Engine) inCooldown(aid int, cfg Config) bool {
	if cfg.MinSyncIntervalSeconds <= 0 {
		return false
	}
	e.mu.Lock()
	last, seen := e.lastSync[aid]
	e.mu.Unlock()
	return seen && time.Since(last) < time.Duration(cfg.MinSyncIntervalSeconds)*time.Second
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
	// 退避期优先表达 deferred（失败重试调度的一部分）
	if state != nil && state.NextRetryAt != "" {
		if t, err := time.Parse(time.RFC3339, state.NextRetryAt); err == nil && time.Now().UTC().Before(t) {
			return false, "deferred", ""
		}
	}
	// 单账号冷却：刚同步过的账号不重复拉取（防止 MCP / 手动在短时间内重复触发）
	sched := e.Config()
	if e.inCooldown(aid, sched) {
		return false, "throttled", "cooldown"
	}
	e.markSynced(aid)

	// 增量同步：带上各端点的 ETag，未变化（304）不消耗 API 配额
	etags := map[string]string{}
	for _, key := range []string{"review_prs", "my_prs", "issues", "events"} {
		if v, err := GetETag(e.db, aid, key); err == nil && v != "" {
			etags[key] = v
		}
	}
	// 同步前的旧状态（用于事件检测与 304 兜底）
	prevMap := summaryPayloadMap(e.db, aid)
	wasCapped := isCapped(rateLimitOf(e.db, aid))
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

	// 出站 Webhook：把本次同步检测到的变化推给外部系统
	for _, ev := range detectSyncEvents(prevMap, st, wasCapped, isCapped(rateLimitOf(e.db, aid))) {
		payload := map[string]any{
			"event": ev[0],
			"account": map[string]any{
				"id":       aid,
				"username": acc.Username,
				"label":    st.Label,
			},
			"detail": ev[1],
			"ts":     ts,
		}
		for _, d := range notify.Dispatch(e.dir, ev[0], payload) {
			LogWebhook(e.db, d.URL, d.EventType, d.OK, d.Detail)
		}
	}
	return st.Status == "active", st.Status, ""
}

// summaryPayloadMap 返回汇总缓存的 payload（解析后的 map）。
func summaryPayloadMap(db *sql.DB, id int) map[string]any {
	row, err := GetSummary(db, id)
	if err != nil || row == nil || len(row.Payload) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(row.Payload, &m) != nil {
		return nil
	}
	return m
}

func rateLimitOf(db *sql.DB, id int) *RateLimit {
	m, err := AllRateLimits(db)
	if err != nil {
		return nil
	}
	rl, ok := m[id]
	if !ok {
		return nil
	}
	return &rl
}

func isCapped(rl *RateLimit) bool {
	return rl != nil && rl.QuotaLimit > 0 && rl.Remaining <= QuotaCap
}

// detectSyncEvents 对比新旧汇总，返回 [(事件类型, 详情)]。
func detectSyncEvents(previous map[string]any, st *service.Status, wasCapped, nowCapped bool) [][2]string {
	events := [][2]string{}
	pairs := []struct {
		eventType string
		key       string
		label     string
		get       func() int
	}{
		{"review_pr.added", "review_prs_count", "待 Review PR", func() int { return st.ReviewPRsCount }},
		{"my_pr.added", "my_prs_count", "我发起的 PR", func() int { return st.MyPRsCount }},
		{"issue.added", "assigned_issues_count", "分配 Issue", func() int { return st.AssignedIssuesCount }},
	}
	for _, p := range pairs {
		oldN := toInt(previous[p.key])
		newN := p.get()
		if newN > oldN {
			events = append(events, [2]string{p.eventType, fmt.Sprintf("%s从 %d 增加到 %d", p.label, oldN, newN)})
		}
	}
	if st.Status == "invalid" || st.Status == "error" {
		events = append(events, [2]string{"account.invalid", orString(st.Error, "凭据无效或已过期")})
	} else if st.Status != "active" {
		events = append(events, [2]string{"sync.failed", orString(st.Error, "同步状态 "+st.Status)})
	}
	if nowCapped && !wasCapped {
		events = append(events, [2]string{"account.capped", "API 已封顶（剩余 ≤ 60/小时）"})
	}
	return events
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
