package ops

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github-multi-account/internal/config"
	"github-multi-account/internal/github"
	"github-multi-account/internal/service"
	"github-multi-account/internal/store"
)

func newTestEnv(t *testing.T) (*config.Store, func()) {
	t.Helper()
	dir := t.TempDir()
	cfg, err := config.Open(dir)
	if err != nil {
		t.Fatalf("打开配置失败: %v", err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	if err := Init(st.DB()); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return cfg, func() { _ = st.Close() }
}

func addAccounts(t *testing.T, cfg *config.Store, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		group := []string{"工作", "个人", "测试"}[i%3]
		typ := []string{"personal", "bot", "org"}[i%3]
		tags := [][]string{{"核心"}, {"自动化", "ci"}, {"实验"}}[i%3]
		if _, err := cfg.Add(config.Account{
			Label:    fmt.Sprintf("账号%d", i),
			Username: fmt.Sprintf("user%d", i),
			Token:    fmt.Sprintf("ghp_fake%04d", i),
			Group:    group,
			Type:     typ,
			Tags:     tags,
			Alias:    fmt.Sprintf("U%d", i),
			Note:     fmt.Sprintf("第%d个测试账号", i),
		}); err != nil {
			t.Fatalf("添加账号失败: %v", err)
		}
	}
}


func TestQueryAccountsPaginationAndFilter(t *testing.T) {
	cfg, cleanup := newTestEnv(t)
	defer cleanup()
	addAccounts(t, cfg, 100)
	accounts, err := cfg.Accounts()
	if err != nil {
		t.Fatalf("读取账号失败: %v", err)
		}
	db := DB()
	for _, a := range accounts {
		status := "active"
		if a.ID%10 == 0 {
			status = "invalid"
		}
		if err := UpsertSummary(db, SummaryRow{
			AccountID: a.ID, Username: a.Username, Status: status, Label: a.Label,
			Type: a.Type, GroupName: a.Group, Tags: a.Tags,
			Followers: a.ID * 10, Following: a.ID, PublicRepos: a.ID % 20,
			ReviewPRs: a.ID % 30, MyPRs: a.ID % 10, AssignedIssues: a.ID % 20,
			TotalStars: a.ID * 100, SyncedAt: "2026-01-01T00:00:00Z",
		}); err != nil {
			t.Fatalf("写入汇总失败: %v", err)
		}
		if err := UpsertSyncState(db, SyncState{
			AccountID: a.ID, Username: a.Username, LastSyncAt: "2026-01-01T00:00:00Z",
			LastStatus: status, SyncEnabled: 1, UpdatedAt: "2026-01-01T00:00:00Z",
		}, nil); err != nil {
			t.Fatalf("写入状态失败: %v", err)
		}
		if err := UpsertRateLimit(db, RateLimit{
			AccountID: a.ID, Username: a.Username,
			Remaining: 5000 - a.ID, QuotaLimit: 5000, UpdatedAt: "2026-01-01T00:00:00Z",
		}); err != nil {
			t.Fatalf("写入配额失败: %v", err)
		}
	}

	r, err := QueryAccounts(db, accounts, AccountQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if r.Total != 100 || len(r.Items) != 50 || r.TotalPages != 2 {
		t.Fatalf("分页不正确: total=%d len=%d pages=%d", r.Total, len(r.Items), r.TotalPages)
	}

	r, _ = QueryAccounts(db, accounts, AccountQuery{Page: 1, PageSize: 5, Query: "user1"})
	if r.Total != 12 { // user1, user10..user19, user100
		t.Fatalf("搜索 user1 应命中 12 个，得到 %d", r.Total)
	}
	if got := QueryTotal(db, accounts, AccountQuery{Query: "user100"}); got != 1 {
		t.Fatalf("搜索 user100 应命中 1 个，得到 %d", got)
	}
	if got := QueryTotal(db, accounts, AccountQuery{Query: "测试账号"}); got != 100 {
		t.Fatalf("搜索备注应命中 100 个，得到 %d", got)
	}
	if got := QueryTotal(db, accounts, AccountQuery{Status: "invalid"}); got != 10 {
		t.Fatalf("筛选 invalid 应命中 10 个，得到 %d", got)
	}
	if got := QueryTotal(db, accounts, AccountQuery{Group: "工作"}); got != 33 {
		t.Fatalf("筛选 工作 应命中 33 个，得到 %d", got)
	}
	if got := QueryTotal(db, accounts, AccountQuery{Tags: "核心"}); got != 33 {
		t.Fatalf("筛选 核心 应命中 33 个，得到 %d", got)
	}

	r, _ = QueryAccounts(db, accounts, AccountQuery{Page: 1, PageSize: 3, Sort: "todos", Order: "desc"})
	if !(r.Items[0].Todos >= r.Items[1].Todos && r.Items[1].Todos >= r.Items[2].Todos) {
		t.Fatalf("按待办降序不正确: %d %d %d", r.Items[0].Todos, r.Items[1].Todos, r.Items[2].Todos)
	}
	r, _ = QueryAccounts(db, accounts, AccountQuery{Page: 1, PageSize: 3, Sort: "quota", Order: "asc"})
	if !(r.Items[0].Remaining <= r.Items[1].Remaining && r.Items[1].Remaining <= r.Items[2].Remaining) {
		t.Fatalf("按配额升序不正确: %d %d %d", r.Items[0].Remaining, r.Items[1].Remaining, r.Items[2].Remaining)
	}
	r, _ = QueryAccounts(db, accounts, AccountQuery{Page: 1, PageSize: 1})
	if len(r.Groups) != 3 || len(r.Tags) != 4 {
		t.Fatalf("分组/标签聚合不正确: %v %v", r.Groups, r.Tags)
	}
}

func QueryTotal(db *sql.DB, accounts []config.Account, q AccountQuery) int {
	r, _ := QueryAccounts(db, accounts, q)
	return r.Total
}

func TestBuildSummaryAndTasks(t *testing.T) {
	cfg, cleanup := newTestEnv(t)
	defer cleanup()
	addAccounts(t, cfg, 3)
	accounts, _ := cfg.Accounts()
	db := DB()

	mustUpsertSummary(t, db, SummaryRow{
		AccountID: 1, Username: "user1", Status: "invalid", Label: "账号1",
		Error: "Token 无效", SyncedAt: "2026-01-01T00:00:00Z",
	})
	mustUpsertState(t, db, SyncState{
		AccountID: 1, Username: "user1", LastStatus: "invalid", LastError: "Token 无效",
		ConsecutiveFailures: 4, LastSyncAt: "2026-01-01T00:00:00Z", SyncEnabled: 1,
		UpdatedAt: "2026-01-01T00:00:00Z",
	})
	mustUpsertSummary(t, db, SummaryRow{
		AccountID: 2, Username: "user2", Status: "active", Label: "账号2",
		ReviewPRs: 5, MyPRs: 2, AssignedIssues: 3, Followers: 100, TotalStars: 500,
		SyncedAt: "2026-01-02T00:00:00Z",
	})
	mustUpsertState(t, db, SyncState{
		AccountID: 2, Username: "user2", LastStatus: "active",
		LastSyncAt: "2026-01-02T00:00:00Z", SyncEnabled: 1, UpdatedAt: "2026-01-02T00:00:00Z",
	})
	mustUpsertSummary(t, db, SummaryRow{
		AccountID: 3, Username: "user3", Status: "active", Label: "账号3",
		SyncedAt: "2026-01-02T00:00:00Z",
	})
	mustUpsertState(t, db, SyncState{
		AccountID: 3, Username: "user3", LastStatus: "active",
		LastSyncAt: "2026-01-02T00:00:00Z", SyncEnabled: 1, UpdatedAt: "2026-01-02T00:00:00Z",
	})

	s, err := BuildSummary(db, accounts)
	if err != nil {
		t.Fatalf("总览失败: %v", err)
	}
	if s.Total != 3 || s.Healthy != 2 || s.Attention != 1 || s.CredentialRisk != 1 {
		t.Fatalf("总览计数不正确: %+v", s)
	}
	if s.ReviewPRs != 5 || s.Issues != 3 || s.MyPRs != 2 {
		t.Fatalf("待办计数不正确: %+v", s)
	}
	if s.LastSyncAt != "2026-01-02T00:00:00Z" {
		t.Fatalf("最近同步时间不正确: %q", s.LastSyncAt)
	}

	tasks, err := BuildTasks(db, accounts)
	if err != nil {
		t.Fatalf("待办失败: %v", err)
	}
	if len(tasks) == 0 || tasks[0].Priority != "high" {
		t.Fatalf("最高优先级应排在最前: %+v", tasks)
	}
	order := map[string]int{"high": 0, "medium": 1, "low": 2}
	for i := 1; i < len(tasks); i++ {
		if order[tasks[i-1].Priority] > order[tasks[i].Priority] {
			t.Fatalf("待办未按优先级排序")
		}
	}
	var hasCredential, hasReview, hasIssue bool
	for _, task := range tasks {
		if task.Type == "credential" && task.AccountLabel == "账号1" {
			hasCredential = true
		}
		if task.Type == "review_pr" && task.AccountLabel == "账号2" {
			hasReview = true
		}
		if task.Type == "issue" && task.AccountLabel == "账号2" {
			hasIssue = true
		}
	}
	if !hasCredential || !hasReview || !hasIssue {
		t.Fatalf("缺少关键待办: credential=%v review=%v issue=%v", hasCredential, hasReview, hasIssue)
	}
}

func TestSyncAccountBackoffAndQuotaGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer srv.Close()
	old := github.BaseURL
	github.BaseURL = srv.URL
	defer func() { github.BaseURL = old }()

	cfg, cleanup := newTestEnv(t)
	defer cleanup()
	if _, err := cfg.Add(config.Account{
		Label: "坏号", Username: "bad", Token: "ghp_bad", Group: "工作", Type: "personal",
	}); err != nil {
		t.Fatalf("添加账号失败: %v", err)
	}
	accounts, _ := cfg.Accounts()
	db := DB()
	eng := NewEngine(db, cfg, &service.Service{}, t.TempDir())
	eng.mu.Lock()
	eng.conf.Concurrency = 1
	eng.conf.QuotaFloor = 0
	eng.mu.Unlock()

	ok, status, _ := eng.SyncAccount(accounts[0])
	if ok || status != "invalid" {
		t.Fatalf("坏号应同步失败: ok=%v status=%q", ok, status)
	}
	st, _ := GetSyncState(db, accounts[0].ID)
	if st == nil || st.ConsecutiveFailures != 1 {
		t.Fatalf("应记录 1 次连续失败: %+v", st)
	}
	if st.NextRetryAt == "" {
		t.Fatal("应写入退避时间")
	}
	// 退避期间再次同步应被推迟
	if ok, status, _ := eng.SyncAccount(accounts[0]); ok || status != "deferred" {
		t.Fatalf("退避期间应推迟: ok=%v status=%q", ok, status)
	}

	// 配额保护：剩余额度低于下限应跳过
	if err := UpsertRateLimit(db, RateLimit{
		AccountID: accounts[0].ID, Username: accounts[0].Username,
		Remaining: 5, QuotaLimit: 5000, UpdatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("写入配额失败: %v", err)
	}
	eng.mu.Lock()
	eng.conf.QuotaFloor = 100
	eng.mu.Unlock()
	// 清掉退避时间，确保走到配额判断
	mustUpsertState(t, db, SyncState{
		AccountID: accounts[0].ID, Username: accounts[0].Username,
		LastStatus: "invalid", SyncEnabled: 1, UpdatedAt: "2026-01-01T00:00:00Z",
	})
	if ok, status, _ := eng.SyncAccount(accounts[0]); ok || status != "skipped" {
		t.Fatalf("低配额应跳过: ok=%v status=%q", ok, status)
	}
}

func mustUpsertSummary(t *testing.T, db *sql.DB, s SummaryRow) {
	t.Helper()
	if err := UpsertSummary(db, s); err != nil {
		t.Fatalf("写入汇总失败: %v", err)
	}
}

func TestSettingsSectionsDoNotOverwriteEachOther(t *testing.T) {
	dir := t.TempDir()
	if err := SaveSettings(dir, DefaultConfig()); err != nil {
		t.Fatalf("写同步设置失败: %v", err)
	}
	// 模拟 notify 保存渠道
	data := config.ReadSettings(dir)
	data["channels"] = []map[string]any{{
		"type": "bark", "name": "iPhone", "url": "https://x", "enabled": true,
	}}
	if err := config.WriteSettings(dir, data); err != nil {
		t.Fatalf("写渠道失败: %v", err)
	}
	// 再写同步设置，渠道应保留
	cfg := DefaultConfig()
	cfg.IntervalSeconds = 600
	if err := SaveSettings(dir, cfg); err != nil {
		t.Fatalf("再写同步设置失败: %v", err)
	}

	raw := config.ReadSettings(dir)
	channels, ok := raw["channels"].([]any)
	if !ok || len(channels) != 1 {
		t.Fatalf("渠道被覆盖: %#v", raw["channels"])
	}
	if loaded := LoadSettings(dir); loaded.IntervalSeconds != 600 {
		t.Fatalf("同步设置丢失: %+v", loaded)
	}
}

func TestDetectSyncEvents(t *testing.T) {
	prev := map[string]any{"review_prs_count": float64(2), "my_prs_count": float64(0)}
	st := &service.Status{Status: "active", ReviewPRsCount: 3, MyPRsCount: 1}

	events := detectSyncEvents(prev, st, false, false)
	types := []string{}
	for _, e := range events {
		types = append(types, e[0])
	}
	if !containsString(types, "review_pr.added") || !containsString(types, "my_pr.added") {
		t.Fatalf("应检测数量增加: %+v", types)
	}
	if containsString(types, "account.invalid") || containsString(types, "sync.failed") {
		t.Fatalf("正常账号不应产生异常事件: %+v", types)
	}

	events = detectSyncEvents(nil, &service.Status{Status: "invalid", Error: "Token 无效"}, false, false)
	if len(events) != 1 || events[0][0] != "account.invalid" {
		t.Fatalf("凭据失效事件不正确: %+v", events)
	}

	events = detectSyncEvents(nil, &service.Status{Status: "active"}, false, true)
	if !containsString(func() []string {
		out := []string{}
		for _, e := range events {
			out = append(out, e[0])
		}
		return out
	}(), "account.capped") {
		t.Fatalf("封顶事件未检测: %+v", events)
	}
}

func containsString(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func TestBuildTasksCrossAccount(t *testing.T) {
	cfg, cleanup := newTestEnv(t)
	defer cleanup()
	addAccounts(t, cfg, 2)
	db := DB()

	item := github.IssueItem{
		Title: "同一个 PR", HTMLURL: "https://github.com/o/r/pull/1",
		RepositoryURL: "https://api.github.com/repos/o/r", Number: 1,
		CreatedAt: "2026-01-02T00:00:00Z",
	}
	payload, err := json.Marshal(map[string]any{"review_prs": []any{item}})
	if err != nil {
		t.Fatalf("构造 payload 失败: %v", err)
	}
	mustUpsertSummary(t, db, SummaryRow{
		AccountID: 1, Username: "user1", Status: "active", Label: "账号1",
		Payload: payload, SyncedAt: "2026-01-02T00:00:00Z",
	})
	mustUpsertSummary(t, db, SummaryRow{
		AccountID: 2, Username: "user2", Status: "active", Label: "账号2",
		Payload: payload, SyncedAt: "2026-01-02T00:00:00Z",
	})

	accounts, _ := cfg.Accounts()
	tasks, err := BuildTasks(db, accounts)
	if err != nil {
		t.Fatalf("生成待办失败: %v", err)
	}
	var cross []Task
	for _, task := range tasks {
		if task.Type == "cross_account" {
			cross = append(cross, task)
		}
	}
	if len(cross) != 1 {
		t.Fatalf("应有 1 条跨账号关联待办: %+v", tasks)
	}
	if cross[0].URL != item.HTMLURL {
		t.Fatalf("关联待办链接不正确: %q", cross[0].URL)
	}
	if !strings.Contains(cross[0].Detail, "2 个账号相关") {
		t.Fatalf("关联详情不正确: %q", cross[0].Detail)
	}
}

func TestQuotaCapped(t *testing.T) {
	cfg, cleanup := newTestEnv(t)
	defer cleanup()
	addAccounts(t, cfg, 3)
	db := DB()

	// 1 号正好 60（封顶），2 号 61（只是偏低），3 号额度 60（未认证封顶）
	mustUpsertRateLimit(t, db, RateLimit{AccountID: 1, Username: "user1", Remaining: 60, QuotaLimit: 5000})
	mustUpsertRateLimit(t, db, RateLimit{AccountID: 2, Username: "user2", Remaining: 61, QuotaLimit: 5000})
	mustUpsertRateLimit(t, db, RateLimit{AccountID: 3, Username: "user3", Remaining: 0, QuotaLimit: 60})
	for i, u := range []string{"user1", "user2", "user3"} {
		mustUpsertSummary(t, db, SummaryRow{
			AccountID: i + 1, Username: u, Status: "active",
			Label: "账号" + itoa(i+1), SyncedAt: "2026-01-02T00:00:00Z",
		})
	}

	accounts, _ := cfg.Accounts()
	r, err := QueryAccounts(db, accounts, AccountQuery{PageSize: 10})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	capped := map[int]bool{}
	for _, item := range r.Items {
		capped[item.ID] = item.QuotaCapped
	}
	if !capped[1] || capped[2] || !capped[3] {
		t.Fatalf("封顶判定不正确: %+v", capped)
	}

	s, err := BuildSummary(db, accounts)
	if err != nil {
		t.Fatalf("总览失败: %v", err)
	}
	if s.QuotaCapped != 2 {
		t.Fatalf("总览封顶数应为 2，得到 %d", s.QuotaCapped)
	}

	tasks, err := BuildTasks(db, accounts)
	if err != nil {
		t.Fatalf("待办失败: %v", err)
	}
	high, medium := 0, 0
	for _, task := range tasks {
		switch task.Type {
		case "quota_capped":
			high++
			if task.Priority != "high" || task.AccountUsername == "" {
				t.Fatalf("封顶待办不正确: %+v", task)
			}
		case "quota":
			medium++
		}
	}
	if high != 2 || medium != 1 {
		t.Fatalf("应 2 条高优先级封顶 + 1 条中优先级偏低（user2），得到 high=%d medium=%d", high, medium)
	}
}

func mustUpsertRateLimit(t *testing.T, db *sql.DB, r RateLimit) {
	t.Helper()
	if err := UpsertRateLimit(db, r); err != nil {
		t.Fatalf("写入配额失败: %v", err)
	}
}

func TestPruneAudit(t *testing.T) {
	_, cleanup := newTestEnv(t)
	defer cleanup()
	db := DB()

	for i := 0; i < 10; i++ {
		Audit(db, "test.action", nil, "", "x")
	}
	items, err := ListAudit(db, 100, "")
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	if len(items) != 10 {
		t.Fatalf("应写入 10 条审计，得到 %d", len(items))
	}

	n, err := PruneAudit(db, 5, 90)
	if err != nil {
		t.Fatalf("清理审计失败: %v", err)
	}
	if n != 5 {
		t.Fatalf("应清理 5 条，得到 %d", n)
	}
	items, _ = ListAudit(db, 100, "")
	if len(items) != 5 {
		t.Fatalf("应保留 5 条，得到 %d", len(items))
	}

	// 保留期之外的一律清掉
	if n, _ := PruneAudit(db, 100, -1); n != 5 {
		t.Fatalf("应清理剩余 5 条，得到 %d", n)
	}
	if items, _ = ListAudit(db, 100, ""); len(items) != 0 {
		t.Fatalf("审计应清空，得到 %d 条", len(items))
	}
}

func TestQueryAccountsAvatar(t *testing.T) {
	cfg, cleanup := newTestEnv(t)
	defer cleanup()
	addAccounts(t, cfg, 2)
	db := DB()
	payload, err := json.Marshal(map[string]any{
		"user": map[string]any{"avatar_url": "https://example.com/a.png"},
	})
	if err != nil {
		t.Fatalf("构造 payload 失败: %v", err)
	}
	mustUpsertSummary(t, db, SummaryRow{
		AccountID: 1, Username: "user1", Status: "active", Label: "账号1",
		Payload: payload, SyncedAt: "2026-01-02T00:00:00Z",
	})
	accounts, _ := cfg.Accounts()
	r, err := QueryAccounts(db, accounts, AccountQuery{PageSize: 10})
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	var first, second string
	for _, item := range r.Items {
		if item.ID == 1 {
			first = item.AvatarURL
		}
		if item.ID == 2 {
			second = item.AvatarURL
		}
	}
	if first != "https://example.com/a.png" {
		t.Fatalf("账号 1 的头像未带上: %q", first)
	}
	if second != "" {
		t.Fatalf("账号 2 无头像应为空字符串: %q", second)
	}
}

func TestBuildActivitiesAggregates(t *testing.T) {
	cfg, cleanup := newTestEnv(t)
	defer cleanup()
	addAccounts(t, cfg, 1)
	db := DB()

	event := map[string]any{
		"type": "PushEvent",
		"actor": map[string]any{
			"login":      "u",
			"avatar_url": "",
		},
		"repo":       map[string]any{"name": "o/r"},
		"payload":    map[string]any{"action": "pushed"},
		"created_at": "2026-01-02T00:00:00Z",
	}
	payload, err := json.Marshal(map[string]any{
		"events": []any{event, event, event},
	})
	if err != nil {
		t.Fatalf("构造 payload 失败: %v", err)
	}
	mustUpsertSummary(t, db, SummaryRow{
		AccountID: 1, Username: "user1", Status: "active", Label: "账号1",
		Payload: payload, SyncedAt: "2026-01-02T00:00:00Z",
	})

	acts, err := BuildActivities(db, 10, "all")
	if err != nil {
		t.Fatalf("汇总动态失败: %v", err)
	}
	if len(acts) != 1 {
		t.Fatalf("重复活动应聚合成 1 条: %+v", acts)
	}
	if acts[0].Count != 3 {
		t.Fatalf("count 应为 3: %+v", acts[0])
	}
	if acts[0].AccountUsername != "user1" {
		t.Fatalf("缺少账号身份: %+v", acts[0])
	}
}

func mustUpsertState(t *testing.T, db *sql.DB, s SyncState) {
	t.Helper()
	if err := UpsertSyncState(db, s, nil); err != nil {
		t.Fatalf("写入状态失败: %v", err)
	}
}
