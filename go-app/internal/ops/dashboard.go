// 全局聚合：账号总览、统一待办、最近活动。
//
// 所有数据都来自本地缓存（summaries / sync_state / rate_limits），
// 不实时请求 GitHub —— 几百个账号时不会打爆 API。
package ops

import (
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github-multi-account/internal/config"
	"github-multi-account/internal/github"
)

// Summary 是账号总览统计。
type Summary struct {
	Total          int    `json:"total"`
	Healthy        int    `json:"healthy"`
	Attention      int    `json:"attention"`
	CredentialRisk int    `json:"credential_risk"`
	QuotaLow       int    `json:"quota_low"`
	QuotaCapped    int    `json:"quota_capped"`
	ReviewPRs      int    `json:"review_prs"`
	MyPRs          int    `json:"my_prs"`
	Issues         int    `json:"issues"`
	LastSyncAt     string `json:"last_sync_at"`
	SyncedAccounts int    `json:"synced_accounts"`
	GeneratedAt    string `json:"generated_at"`
}

// Task 是一条统一待办。
type Task struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	TypeLabel       string `json:"type_label"`
	Priority        string `json:"priority"`
	Title           string `json:"title"`
	Detail          string `json:"detail"`
	AccountID       int    `json:"account_id"`
	AccountLabel    string `json:"account_label"`
	AccountUsername string `json:"account_username"`
	Time            string `json:"time"`
	Action          string `json:"action"`
	// URL 非空时表示这是一条跨账号关联事项，点击直接打开该链接
	URL string `json:"url,omitempty"`
}

// Activity 是一条跨账号动态（重复活动已聚合，Count 表示同类条数）。
type Activity struct {
	AccountID       int    `json:"account_id"`
	AccountUsername string `json:"account_username"`
	AccountLabel    string `json:"account_label"`
	Type            string `json:"type"`
	Actor           string `json:"actor"`
	ActorAvatar     string `json:"actor_avatar"`
	Repo            string `json:"repo"`
	Action          string `json:"action"`
	Count           int    `json:"count"`
	CreatedAt       string `json:"created_at"`
}

var taskTypeLabels = map[string]string{
	"credential":   "凭据异常",
	"quota":        "API 配额不足",
	"quota_capped": "API 已封顶",
	"review_pr":    "待我 Review 的 PR",
	"my_pr":        "我发起的 PR",
	"issue":        "分配给我的 Issue",
	"sync_failed":  "同步失败",
	"sync_stale":   "长期未同步",
}

var priorityOrder = map[string]int{"high": 0, "medium": 1, "low": 2}

const quotaLowThreshold = 100
const staleAfter = 6 * time.Hour

// BuildSummary 汇总账号总览。
func BuildSummary(db *sql.DB, accounts []config.Account) (*Summary, error) {
	summaries, err := AllSummaries(db)
	if err != nil {
		return nil, err
	}
	states, err := AllSyncStates(db)
	if err != nil {
		return nil, err
	}
	limits, err := AllRateLimits(db)
	if err != nil {
		return nil, err
	}
	sm := make(map[int]SummaryRow, len(summaries))
	for _, s := range summaries {
		sm[s.AccountID] = s
	}
	tm := make(map[int]SyncState, len(states))
	for _, s := range states {
		tm[s.AccountID] = s
	}

	out := &Summary{
		Total:          len(accounts),
		SyncedAccounts: len(summaries),
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	for _, a := range accounts {
		s, hasS := sm[a.ID]
		st := tm[a.ID]
		status := "unknown"
		if hasS {
			status = s.Status
		} else if st.LastStatus != "" {
			status = st.LastStatus
		}
		if status == "active" {
			out.Healthy++
		} else {
			out.Attention++
		}
		if status == "invalid" || status == "error" {
			out.CredentialRisk++
		}
		out.ReviewPRs += s.ReviewPRs
		out.MyPRs += s.MyPRs
		out.Issues += s.AssignedIssues
		if rl, ok := limits[a.ID]; ok {
			if rl.Remaining < quotaLowThreshold {
				out.QuotaLow++
			}
			if rl.QuotaLimit > 0 && rl.Remaining <= QuotaCap {
				out.QuotaCapped++
			}
		}
		if st.LastSyncAt != "" && st.LastSyncAt > out.LastSyncAt {
			out.LastSyncAt = st.LastSyncAt
		}
	}
	return out, nil
}

// BuildTasks 按优先级聚合所有账号的待处理事项。
func BuildTasks(db *sql.DB, accounts []config.Account) ([]Task, error) {
	summaries, err := AllSummaries(db)
	if err != nil {
		return nil, err
	}
	states, err := AllSyncStates(db)
	if err != nil {
		return nil, err
	}
	limits, err := AllRateLimits(db)
	if err != nil {
		return nil, err
	}
	accs := make(map[int]config.Account, len(accounts))
	for _, a := range accounts {
		accs[a.ID] = a
	}

	tasks := []Task{}
	for _, s := range summaries {
		acc := accs[s.AccountID]
		label := s.Label
		if label == "" {
			label = acc.Label
		}
		if label == "" {
			label = acc.Username
		}
		if label == "" {
			label = "账号 " + itoa(s.AccountID)
		}
		st := statesByIndex(states, s.AccountID)
		rl, hasRL := limits[s.AccountID]
		syncedAt := st.LastSyncAt

		if s.Status == "invalid" || s.Status == "error" {
			tasks = append(tasks, newTask("credential", "high", "凭据无效或已过期",
				orString(s.Error, "登录失败，请检查 Token"), label, s.AccountID, acc.Username, syncedAt, "重新录入凭据"))
		}
		if hasRL {
			if rl.QuotaLimit > 0 && rl.Remaining <= QuotaCap {
				tasks = append(tasks, newTask("quota_capped", "high", "API 已封顶",
					itoa(rl.Remaining)+"/"+itoa(rl.QuotaLimit)+"（未认证额度仅 60/小时）",
					label, s.AccountID, acc.Username, rl.UpdatedAt, "检查凭据"))
			} else if rl.Remaining < quotaLowThreshold {
				tasks = append(tasks, newTask("quota", "medium", "API 配额不足",
					itoa(rl.Remaining)+"/"+itoa(rl.QuotaLimit), label, s.AccountID, acc.Username, rl.UpdatedAt, "查看配额"))
			}
		}
		if s.ReviewPRs > 0 {
			tasks = append(tasks, newTask("review_pr", "medium",
				itoa(s.ReviewPRs)+" 个 PR 等待你 Review", "有人请求你审查代码",
				label, s.AccountID, acc.Username, syncedAt, "去 Review"))
		}
		if s.MyPRs > 0 {
			tasks = append(tasks, newTask("my_pr", "low",
				itoa(s.MyPRs)+" 个我发起的 PR 未关闭", "关注 CI 与评审进展",
				label, s.AccountID, acc.Username, syncedAt, "查看"))
		}
		if s.AssignedIssues > 0 {
			tasks = append(tasks, newTask("issue", "medium",
				itoa(s.AssignedIssues)+" 个 Issue 待处理", "分配给你的未关闭 Issue",
				label, s.AccountID, acc.Username, syncedAt, "去处理"))
		}
		if st.ConsecutiveFailures >= 3 {
			tasks = append(tasks, newTask("sync_failed", "high", "连续多次同步失败",
				orString(st.LastError, "请检查网络与凭据"), label, s.AccountID, acc.Username, st.UpdatedAt, "立即重试"))
		}
		if syncedAt != "" {
			if t, err := time.Parse(time.RFC3339, syncedAt); err == nil {
				if time.Since(t) > staleAfter {
					tasks = append(tasks, newTask("sync_stale", "low", "超过 6 小时未同步",
						"上次同步 "+syncedAt, label, s.AccountID, acc.Username, syncedAt, "立即同步"))
				}
			}
		}
	}

	// 跨账号关联：同一 PR / Issue 被多个账号看到时合并成一条，避免重复
	type crossEntry struct {
		accounts map[int]bool
		item     github.IssueItem
	}
	itemAccounts := map[string]*crossEntry{}
	addCross := func(aid int, it github.IssueItem) {
		if it.HTMLURL == "" {
			return
		}
		e := itemAccounts[it.HTMLURL]
		if e == nil {
			e = &crossEntry{accounts: map[int]bool{}, item: it}
			itemAccounts[it.HTMLURL] = e
		}
		e.accounts[aid] = true
	}
	for otherID, s := range summaries {
		if len(s.Payload) == 0 {
			continue
		}
		var payload struct {
			ReviewPRs      []github.IssueItem `json:"review_prs"`
			MyPRs          []github.IssueItem `json:"my_prs"`
			AssignedIssues []github.IssueItem `json:"assigned_issues"`
		}
		if json.Unmarshal(s.Payload, &payload) != nil {
			continue
		}
		for _, it := range payload.ReviewPRs {
			addCross(otherID, it)
		}
		for _, it := range payload.MyPRs {
			addCross(otherID, it)
		}
		for _, it := range payload.AssignedIssues {
			addCross(otherID, it)
		}
	}

	for url, entry := range itemAccounts {
		if len(entry.accounts) < 2 {
			continue
		}
		ids := make([]int, 0, len(entry.accounts))
		for aid := range entry.accounts {
			ids = append(ids, aid)
		}
		sort.Ints(ids)
		labels := make([]string, 0, len(ids))
		for _, aid := range ids {
			labels = append(labels, accountLabel(summaries[aid], accounts[aid]))
		}
		item := entry.item
		tasks = append(tasks, Task{
			ID:              "cross:" + url,
			Type:            "cross_account",
			TypeLabel:       "跨账号关联",
			Priority:        "medium",
			Title:           orString(item.Title, url),
			Detail:          repoShort(item.RepositoryURL) + "#" + itoa(item.Number) + " · " + itoa(len(entry.accounts)) + " 个账号相关",
			AccountID:       0,
			AccountLabel:    strings.Join(labels, "、"),
			AccountUsername: "",
			Time:            item.CreatedAt,
			Action:          "打开",
			URL:             url,
		})
	}

	sort.SliceStable(tasks, func(i, j int) bool {
		pi := priorityOrder[tasks[i].Priority]
		pj := priorityOrder[tasks[j].Priority]
		if pi != pj {
			return pi < pj
		}
		return tasks[i].Time < tasks[j].Time
	})
	return tasks, nil
}

// ClusterInfo 是一个画像簇。
type ClusterInfo struct {
	Index    int       `json:"index"`
	Label    string    `json:"label"`
	Size     int       `json:"size"`
	Centroid []float64 `json:"centroid"`
}

// ProfileAccount 是一个账号的画像结果。
type ProfileAccount struct {
	AccountID    int       `json:"account_id"`
	Cluster      int       `json:"cluster"`
	ClusterLabel string    `json:"cluster_label"`
	Features     []float64 `json:"features"`
}

// ProfilesResult 是画像聚类结果。
type ProfilesResult struct {
	Clusters []ClusterInfo    `json:"clusters"`
	Accounts []ProfileAccount `json:"accounts"`
}

// BuildProfiles 账号画像：按影响力 / 活跃度自动聚类（k-means，k=3）。
// 特征：粉丝、总 Star、公开仓库、待 Review PR、我的 PR、分配 Issue、动态数。
func BuildProfiles(db *sql.DB, accounts []config.Account) (*ProfilesResult, error) {
	summaries, err := AllSummaries(db)
	if err != nil {
		return nil, err
	}
	sm := make(map[int]SummaryRow, len(summaries))
	for _, s := range summaries {
		sm[s.AccountID] = s
	}

	ids := []int{}
	vectors := [][]float64{}
	for _, a := range accounts {
		s := sm[a.ID]
		var payload struct {
			Events []json.RawMessage `json:"events"`
		}
		if len(s.Payload) > 0 {
			_ = json.Unmarshal(s.Payload, &payload)
		}
		vectors = append(vectors, []float64{
			float64(s.Followers), float64(s.TotalStars), float64(s.PublicRepos),
			float64(s.ReviewPRs), float64(s.MyPRs), float64(s.AssignedIssues),
			float64(len(payload.Events)),
		})
		ids = append(ids, a.ID)
	}
	if len(vectors) == 0 {
		return &ProfilesResult{Clusters: []ClusterInfo{}, Accounts: []ProfileAccount{}}, nil
	}

	dim := len(vectors[0])
	maxes := make([]float64, dim)
	for i := 0; i < dim; i++ {
		m := 0.0
		for _, v := range vectors {
			if v[i] > m {
				m = v[i]
			}
		}
		if m == 0 {
			m = 1
		}
		maxes[i] = m
	}
	norm := make([][]float64, len(vectors))
	for i, v := range vectors {
		norm[i] = make([]float64, dim)
		for j := range v {
			norm[i][j] = v[j] / maxes[j]
		}
	}

	k := 3
	if len(norm) < k {
		k = len(norm)
	}
	centroids, assign := kmeans(norm, k, 25)

	order := make([]int, k)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool { return sumFloat(centroids[order[i]]) > sumFloat(centroids[order[j]]) })
	names := []string{"核心", "活跃", "沉寂"}
	labels := map[int]string{}
	for rank, c := range order {
		if rank < len(names) {
			labels[c] = names[rank]
		} else {
			labels[c] = "簇" + itoa(rank)
		}
	}

	out := &ProfilesResult{Clusters: []ClusterInfo{}, Accounts: []ProfileAccount{}}
	ts := time.Now().UTC().Format(time.RFC3339)
	for i, aid := range ids {
		feats, _ := json.Marshal(vectors[i])
		_ = UpsertProfile(db, ProfileRow{
			AccountID: aid, ClusterIndex: assign[i], ClusterLabel: labels[assign[i]],
			Features: string(feats), UpdatedAt: ts,
		})
		out.Accounts = append(out.Accounts, ProfileAccount{
			AccountID: aid, Cluster: assign[i],
			ClusterLabel: labels[assign[i]], Features: vectors[i],
		})
	}
	for c := 0; c < k; c++ {
		size := 0
		for _, a := range assign {
			if a == c {
				size++
			}
		}
		centroid := make([]float64, dim)
		for i, x := range centroids[c] {
			centroid[i] = math.Round(x*10000) / 10000
		}
		out.Clusters = append(out.Clusters, ClusterInfo{Index: c, Label: labels[c], Size: size, Centroid: centroid})
	}
	return out, nil
}

func distVec(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		d := a[i] - b[i]
		s += d * d
	}
	return math.Sqrt(s)
}

func sumFloat(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s
}

// kmeans 用最远点初始化，返回 (质心, 每个点的簇号)。
func kmeans(vectors [][]float64, k, iterations int) ([][]float64, []int) {
	n := len(vectors)
	dim := len(vectors[0])
	centroids := [][]float64{append([]float64{}, vectors[0]...)}
	for len(centroids) < k {
		best, bestD := 0, -1.0
		for i, v := range vectors {
			d := math.MaxFloat64
			for _, c := range centroids {
				if x := distVec(v, c); x < d {
					d = x
				}
			}
			if d > bestD {
				best, bestD = i, d
			}
		}
		centroids = append(centroids, append([]float64{}, vectors[best]...))
	}
	assign := make([]int, n)
	for it := 0; it < iterations; it++ {
		changed := false
		for i, v := range vectors {
			best, bestD := 0, math.MaxFloat64
			for c, cent := range centroids {
				if d := distVec(v, cent); d < bestD {
					best, bestD = c, d
				}
			}
			if best != assign[i] {
				assign[i] = best
				changed = true
			}
		}
		for c := 0; c < k; c++ {
			members := [][]float64{}
			for i := range vectors {
				if assign[i] == c {
					members = append(members, vectors[i])
				}
			}
			if len(members) > 0 {
				centroids[c] = make([]float64, dim)
				for _, m := range members {
					for d := 0; d < dim; d++ {
						centroids[c][d] += m[d]
					}
				}
				for d := 0; d < dim; d++ {
					centroids[c][d] /= float64(len(members))
				}
			}
		}
		if !changed {
			break
		}
	}
	return centroids, assign
}

// GraphNode 是关系图谱中的一个节点。
type GraphNode struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	Label     string `json:"label"`
	AvatarURL string `json:"avatar_url"`
}

// GraphEdge 是关系图谱中的一条边。
type GraphEdge struct {
	Source int    `json:"source"`
	Target int    `json:"target"`
	Type   string `json:"type"`
	Label  string `json:"label"`
}

// Graph 是账号关系图谱。
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

// BuildGraph 构建账号关系图谱：关注 / 共同仓库 / 共同关注。
// 边数据来自慢速同步入库的 social 与 account_repo。
func BuildGraph(db *sql.DB, accounts []config.Account) (*Graph, error) {
	social, err := AllSocial(db)
	if err != nil {
		return nil, err
	}
	repos, err := AllRepos(db)
	if err != nil {
		return nil, err
	}
	summaries, err := AllSummaries(db)
	if err != nil {
		return nil, err
	}
	avatars := map[int]string{}
	for _, s := range summaries {
		var payload struct {
			User *struct {
				AvatarURL string `json:"avatar_url"`
			} `json:"user"`
		}
		if len(s.Payload) > 0 && json.Unmarshal(s.Payload, &payload) == nil && payload.User != nil {
			avatars[s.AccountID] = payload.User.AvatarURL
		}
	}

	nodes := []GraphNode{}
	for _, a := range accounts {
		nodes = append(nodes, GraphNode{
			ID: a.ID, Username: a.Username,
			Label:     orString(a.Label, a.Username),
			AvatarURL: avatars[a.ID],
		})
	}

	loginToID := map[string]int{}
	for _, a := range accounts {
		loginToID[strings.ToLower(a.Username)] = a.ID
	}
	following := map[int]map[string]bool{}
	for _, row := range social {
		if row.Kind != "following" {
			continue
		}
		if following[row.AccountID] == nil {
			following[row.AccountID] = map[string]bool{}
		}
		following[row.AccountID][strings.ToLower(row.Login)] = true
	}

	edges := []GraphEdge{}
	// 按 (节点对, 类型) 去重：同一对账号可能同时存在关注与共同仓库关系
	seen := map[[3]int]bool{}
	addEdge := func(src, dst, typeCode int, typ, label string) {
		if src == dst {
			return
		}
		lo, hi := src, dst
		if lo > hi {
			lo, hi = hi, lo
		}
		key := [3]int{lo, hi, typeCode}
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, GraphEdge{Source: lo, Target: hi, Type: typ, Label: label})
	}

	for aid, follows := range following {
		for login := range follows {
			if target, ok := loginToID[login]; ok {
				addEdge(aid, target, 1, "follows", "关注")
			}
		}
	}

	repoOwners := map[string][]int{}
	for _, row := range repos {
		key := strings.ToLower(row.Name)
		repoOwners[key] = append(repoOwners[key], row.AccountID)
	}
	for name, owners := range repoOwners {
		if len(owners) < 2 {
			continue
		}
		sort.Ints(owners)
		for i := 0; i < len(owners); i++ {
			for j := i + 1; j < len(owners); j++ {
				addEdge(owners[i], owners[j], 2, "shared_repo", "共同仓库 "+name)
			}
		}
	}

	ids := make([]int, 0, len(following))
	for id := range following {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			common := 0
			for login := range following[ids[i]] {
				if following[ids[j]][login] {
					common++
				}
			}
			if common >= 3 {
				addEdge(ids[i], ids[j], 3, "shared_following", "共同关注 "+itoa(common))
			}
		}
	}

	return &Graph{Nodes: nodes, Edges: edges}, nil
}

// BuildActivities 跨账号汇总最近动态（来自缓存的 events），并聚合重复活动。
func BuildActivities(db *sql.DB, limit int, typeFilter string) ([]Activity, error) {
	summaries, err := AllSummaries(db)
	if err != nil {
		return nil, err
	}
	collected := []Activity{}
	for _, s := range summaries {
		var payload struct {
			Events []struct {
				Type      string `json:"type"`
				Actor     struct {
					Login     string `json:"login"`
					AvatarURL string `json:"avatar_url"`
				} `json:"actor"`
				Repo struct {
					Name string `json:"name"`
				} `json:"repo"`
				Payload   map[string]any `json:"payload"`
				CreatedAt string         `json:"created_at"`
			} `json:"events"`
		}
		if len(s.Payload) == 0 {
			continue
		}
		if err := json.Unmarshal(s.Payload, &payload); err != nil {
			continue
		}
		label := s.Label
		if label == "" {
			label = s.Username
		}
		for _, ev := range payload.Events {
			if typeFilter == "pr" && !strings.Contains(ev.Type, "PullRequest") {
				continue
			}
			if typeFilter == "issue" && !strings.Contains(ev.Type, "Issue") {
				continue
			}
			action, _ := ev.Payload["action"].(string)
			collected = append(collected, Activity{
				AccountID:       s.AccountID,
				AccountUsername: s.Username,
				AccountLabel:    label,
				Type:            ev.Type,
				Actor:           ev.Actor.Login,
				ActorAvatar:     ev.Actor.AvatarURL,
				Repo:            ev.Repo.Name,
				Action:          action,
				CreatedAt:       ev.CreatedAt,
			})
		}
	}

	// 聚合重复活动：同账号 + 同仓库 + 同类型 + 同动作
	grouped := map[activityKey]*Activity{}
	order := []activityKey{}
	for _, a := range collected {
		key := activityKey{accountID: a.AccountID, repo: a.Repo, typ: a.Type, action: a.Action}
		if cur, ok := grouped[key]; ok {
			cur.Count++
			if a.CreatedAt > cur.CreatedAt {
				cur.CreatedAt = a.CreatedAt
			}
			continue
		}
		item := a
		item.Count = 1
		grouped[key] = &item
		order = append(order, key)
	}
	out := make([]Activity, 0, len(order))
	for _, k := range order {
		out = append(out, *grouped[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type activityKey struct {
	accountID int
	repo      string
	typ       string
	action    string
}

func newTask(type_, priority, title, detail, label string, accountID int, username, t, action string) Task {
	return Task{
		ID:              type_ + ":" + itoa(accountID),
		Type:            type_,
		TypeLabel:       taskTypeLabels[type_],
		Priority:        priority,
		Title:           title,
		Detail:          detail,
		AccountID:       accountID,
		AccountLabel:    label,
		AccountUsername: username,
		Time:            t,
		Action:          action,
	}
}

func statesByIndex(states []SyncState, id int) SyncState {
	for _, s := range states {
		if s.AccountID == id {
			return s
		}
	}
	return SyncState{}
}

func orString(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// accountLabel 返回账号的展示名（缓存 label 优先，其次配置）。
func accountLabel(s SummaryRow, acc config.Account) string {
	if s.Label != "" {
		return s.Label
	}
	if acc.Label != "" {
		return acc.Label
	}
	if acc.Username != "" {
		return acc.Username
	}
	return ""
}

// repoShort 从 repository_url 提取 owner/repo。
func repoShort(url string) string {
	parts := strings.Split(strings.Trim(url, "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return url
}
