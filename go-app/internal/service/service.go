// Package service 聚合 GitHub 数据：登录态、PR、Issue、动态、仓库、粉丝。
//
// 容错原则：并发请求中任意一个失败，只降级该字段，不拖垮整个账号。
package service

import (
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github-multi-account/internal/config"
	"github-multi-account/internal/github"
	"github-multi-account/internal/store"
)

const maxConcurrentAccounts = 3

// TopRepo 是 Star 排行榜中的一项。
type TopRepo struct {
	Name  string `json:"name"`
	Stars int    `json:"stars"`
	URL   string `json:"html_url"`
	Lang  string `json:"language"`
}

// DistItem 是 Star 分布中的一项。
type DistItem struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// Status 是一个账号的完整状态（汇总是它的轻量子集）。
type Status struct {
	ID                  int                `json:"id"`
	Label               string             `json:"label"`
	Username            string             `json:"username"`
	TokenMasked         string             `json:"token_masked"`
	Status              string             `json:"status"`
	Type                string             `json:"type"`
	GroupName           string             `json:"group_name"`
	Tags                []string           `json:"tags"`
	User                *github.User       `json:"user"`
	Error               string             `json:"error"`
	Warnings            []string           `json:"warnings"`
	LastChecked         string             `json:"last_checked"`
	ReviewPRs           []github.IssueItem `json:"review_prs"`
	MyPRs               []github.IssueItem `json:"my_prs"`
	AssignedIssues      []github.IssueItem `json:"assigned_issues"`
	ReviewPRsCount      int                `json:"review_prs_count"`
	MyPRsCount          int                `json:"my_prs_count"`
	AssignedIssuesCount int                `json:"assigned_issues_count"`
	Events              []github.Event     `json:"events"`
	Repos               []github.Repo      `json:"repos"`
	RateLimit           map[string]any     `json:"rate_limit"`
	FollowersCount      int                `json:"followers_count"`
	FollowingCount      int                `json:"following_count"`
	PublicRepos         int                `json:"public_repos"`
	TotalStars          int                `json:"total_stars"`
	TopRepos            []TopRepo          `json:"top_repos"`
	StarDistribution    []DistItem         `json:"star_distribution"`
	FollowerList        []github.Person    `json:"follower_list"`
	FollowingList       []github.Person    `json:"following_list"`
}

// Service 聚合服务。
type Service struct {
	Store *store.Store
}

func emptyStatus(acc config.Account) *Status {
	typ := acc.Type
	if typ == "" {
		typ = "personal"
	}
	return &Status{
		ID:               acc.ID,
		Label:            acc.Label,
		Username:         acc.Username,
		TokenMasked:      acc.MaskedToken(),
		Status:           "unknown",
		Type:             typ,
		GroupName:        acc.Group,
		Tags:             nonNil(acc.Tags),
		Warnings:         []string{},
		LastChecked:      time.Now().UTC().Format(time.RFC3339),
		ReviewPRs:        []github.IssueItem{},
		MyPRs:            []github.IssueItem{},
		AssignedIssues:   []github.IssueItem{},
		Events:           []github.Event{},
		Repos:            []github.Repo{},
		TopRepos:         []TopRepo{},
		StarDistribution: []DistItem{},
		FollowerList:     []github.Person{},
		FollowingList:    []github.Person{},
	}
}

func statusFromError(err error) string {
	var ge *github.Error
	if errors.As(err, &ge) && ge.Status == 401 {
		return "invalid"
	}
	return "error"
}

// Verify 仅验证 Token 有效性。
func Verify(username, token string) (string, *github.User, string) {
	if token == "" {
		return "invalid", nil, "未配置 Token"
	}
	u, err := github.New(token).User()
	if err != nil {
		return statusFromError(err), nil, err.Error()
	}
	return "active", u, ""
}

// Summary 轻量汇总：登录态 + PR/Issue 计数（侧栏用）。
func (s *Service) Summary(acc config.Account) *Status {
	st := emptyStatus(acc)
	if acc.Token == "" {
		st.Status = "invalid"
		st.Error = "未配置 Token"
		return st
	}
	c := github.New(acc.Token)
	user, err := c.User()
	if err != nil {
		st.Status = statusFromError(err)
		st.Error = err.Error()
		return st
	}
	st.User = user
	st.Status = "active"
	login := user.Login

	var (
		mu          sync.Mutex
		failures    []string
		reviewTotal int
		reviewItems []github.IssueItem
		myTotal     int
		myItems     []github.IssueItem
		issueTotal  int
		issueItems  []github.IssueItem
		wg          sync.WaitGroup
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		t, items, err := c.ReviewRequestedPRs(login, 30)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			failures = append(failures, "待处理 PR")
			return
		}
		reviewTotal, reviewItems = t, items
	}()
	go func() {
		defer wg.Done()
		t, items, err := c.MyOpenPRs(login, 30)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			failures = append(failures, "我的 PR")
			return
		}
		myTotal, myItems = t, items
	}()
	go func() {
		defer wg.Done()
		t, items, err := c.AssignedIssues(login, 30)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			failures = append(failures, "分配 Issue")
			return
		}
		issueTotal, issueItems = t, items
	}()
	wg.Wait()

	if len(failures) > 0 {
		st.Warnings = failures
	}
	st.ReviewPRs = nonNil(reviewItems)
	st.MyPRs = nonNil(myItems)
	st.AssignedIssues = nonNil(issueItems)
	st.ReviewPRsCount = reviewTotal
	st.MyPRsCount = myTotal
	st.AssignedIssuesCount = issueTotal
	// 账号对比用的轻量指标（都来自 /user，无需额外请求）
	st.FollowersCount = user.Followers
	st.FollowingCount = user.Following
	st.PublicRepos = user.PublicRepos
	if len(failures) > 0 {
		log.Printf("账号 %s 汇总部分失败: %v", acc.Username, failures)
	}
	return st
}

// SyncSummary 是后台同步用的汇总：在 Summary 的基础上额外拉取最近动态与 API 配额，
// 结果写入本地缓存供仪表盘读取（不实时请求 GitHub）。
// SyncSummary 是全量同步汇总（不启用条件请求）。
func (s *Service) SyncSummary(acc config.Account) *Status {
	return s.syncSummary(acc, nil, nil)
}

// SyncSummaryCond 是增量同步汇总：etags 非空时启用 ETag 条件请求，
// 304 表示未变化（不消耗 API 配额），直接用 previous 里的旧值。
func (s *Service) SyncSummaryCond(acc config.Account, etags map[string]string, previous *Status) *Status {
	return s.syncSummary(acc, etags, previous)
}

func prevSearch(prev *Status, idx int) (int, []github.IssueItem) {
	if prev == nil {
		return 0, nil
	}
	switch idx {
	case 0:
		return prev.ReviewPRsCount, prev.ReviewPRs
	case 1:
		return prev.MyPRsCount, prev.MyPRs
	default:
		return prev.AssignedIssuesCount, prev.AssignedIssues
	}
}

func (s *Service) syncSummary(acc config.Account, etags map[string]string, previous *Status) *Status {
	st := emptyStatus(acc)
	if acc.Token == "" {
		st.Status = "invalid"
		st.Error = "未配置 Token"
		return st
	}
	c := github.New(acc.Token)
	user, err := c.User()
	if err != nil {
		st.Status = statusFromError(err)
		st.Error = err.Error()
		return st
	}
	st.User = user
	st.Status = "active"
	login := user.Login
	useCond := etags != nil

	var (
		mu          sync.Mutex
		failures    []string
		reviewTotal int
		reviewItems []github.IssueItem
		myTotal     int
		myItems     []github.IssueItem
		issueTotal  int
		issueItems  []github.IssueItem
		events      []github.Event
		wg          sync.WaitGroup
	)

	apply := func(idx int, total int, items []github.IssueItem) {
		switch idx {
		case 0:
			reviewTotal, reviewItems = total, items
		case 1:
			myTotal, myItems = total, items
		case 2:
			issueTotal, issueItems = total, items
		}
	}

	fetchSearch := func(idx int, name, query, ekey string, prevTotal int, prevItems []github.IssueItem) {
		defer wg.Done()
		var (
			total   int
			items   []github.IssueItem
			newETag string
			mod     = true
			err     error
		)
		if useCond {
			total, items, newETag, mod, err = c.SearchCond(query, 30, etags[ekey])
		} else {
			total, items, err = c.Search(query, 30)
		}
		mu.Lock()
		defer mu.Unlock()
		if newETag != "" {
			etags[ekey] = newETag
		}
		if err != nil {
			failures = append(failures, name)
			return
		}
		if useCond && !mod {
			apply(idx, prevTotal, prevItems)
			return
		}
		apply(idx, total, items)
	}

	prev0t, prev0i := prevSearch(previous, 0)
	prev1t, prev1i := prevSearch(previous, 1)
	prev2t, prev2i := prevSearch(previous, 2)

	wg.Add(4)
	go fetchSearch(0, "待处理 PR", "is:open is:pr review-requested:"+login, "review_prs", prev0t, prev0i)
	go fetchSearch(1, "我的 PR", "is:open is:pr author:"+login, "my_prs", prev1t, prev1i)
	go fetchSearch(2, "分配 Issue", "is:open is:issue assignee:"+login, "issues", prev2t, prev2i)
	go func() {
		defer wg.Done()
		var (
			v       []github.Event
			newETag string
			mod     = true
			err     error
		)
		if useCond {
			v, newETag, mod, err = c.ReceivedEventsCond(login, 30, etags["events"])
		} else {
			v, err = c.ReceivedEvents(login, 30)
		}
		mu.Lock()
		defer mu.Unlock()
		if newETag != "" {
			etags["events"] = newETag
		}
		if err != nil {
			failures = append(failures, "仓库动态")
			return
		}
		if useCond && !mod {
			if previous != nil {
				events = previous.Events
			}
			return
		}
		events = v
	}()
	wg.Wait()

	if len(failures) > 0 {
		st.Warnings = failures
	}
	st.ReviewPRs = nonNil(reviewItems)
	st.MyPRs = nonNil(myItems)
	st.AssignedIssues = nonNil(issueItems)
	st.ReviewPRsCount = reviewTotal
	st.MyPRsCount = myTotal
	st.AssignedIssuesCount = issueTotal
	st.Events = nonNil(events)
	st.FollowersCount = user.Followers
	st.FollowingCount = user.Following
	st.PublicRepos = user.PublicRepos

	// 速率限制（失败不影响主流程）
	if rl, err := c.RateLimit(); err == nil {
		st.RateLimit = rl
	}
	if len(failures) > 0 {
		log.Printf("账号 %s 同步汇总部分失败: %v", acc.Username, failures)
	}
	return st
}

// FetchSocial 拉取粉丝 / 关注 / 代表仓库（用于关系图谱，慢速同步）。
func (s *Service) FetchSocial(acc config.Account) (followers, following []github.Person, repos []github.Repo, err error) {
	if acc.Token == "" {
		return nil, nil, nil, nil
	}
	c := github.New(acc.Token)
	user, e := c.User()
	if e != nil {
		return nil, nil, nil, e
	}
	login := user.Login
	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		v, _ := c.Followers(login, 100)
		mu.Lock()
		followers = v
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		v, _ := c.Following(login, 100)
		mu.Lock()
		following = v
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		v, _ := c.UserReposAll(login, 2, 3)
		mu.Lock()
		repos = v
		mu.Unlock()
	}()
	wg.Wait()
	return followers, following, repos, nil
}

// Detail 完整详情：概览 / PR / Issue / 动态 / 仓库 / 粉丝。
func (s *Service) Detail(acc config.Account, forceRecord bool) *Status {
	st := emptyStatus(acc)
	if acc.Token == "" {
		st.Status = "invalid"
		st.Error = "未配置 Token"
		return st
	}
	c := github.New(acc.Token)
	user, err := c.User()
	if err != nil {
		st.Status = statusFromError(err)
		st.Error = err.Error()
		return st
	}
	st.User = user
	st.Status = "active"
	login := user.Login

	var (
		mu          sync.Mutex
		failures    []string
		reviewTotal int
		reviewItems []github.IssueItem
		myTotal     int
		myItems     []github.IssueItem
		issueTotal  int
		issueItems  []github.IssueItem
		events      []github.Event
		repos       []github.Repo
		followers   []github.Person
		following   []github.Person
		wg          sync.WaitGroup
	)
	record := func(name string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			failures = append(failures, name)
		}
	}
	wg.Add(7)
	go func() {
		defer wg.Done()
		t, items, err := c.ReviewRequestedPRs(login, 30)
		record("待处理 PR", err)
		if err == nil {
			mu.Lock()
			reviewTotal, reviewItems = t, items
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		t, items, err := c.MyOpenPRs(login, 30)
		record("我的 PR", err)
		if err == nil {
			mu.Lock()
			myTotal, myItems = t, items
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		t, items, err := c.AssignedIssues(login, 30)
		record("分配 Issue", err)
		if err == nil {
			mu.Lock()
			issueTotal, issueItems = t, items
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		v, err := c.ReceivedEvents(login, 30)
		record("仓库动态", err)
		if err == nil {
			mu.Lock()
			events = v
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		v, err := c.UserReposAll(login, 10, 5)
		record("仓库列表", err)
		if err == nil {
			mu.Lock()
			repos = v
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		v, err := c.Followers(login, 100)
		record("粉丝", err)
		if err == nil {
			mu.Lock()
			followers = v
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		v, err := c.Following(login, 100)
		record("关注", err)
		if err == nil {
			mu.Lock()
			following = v
			mu.Unlock()
		}
	}()
	wg.Wait()

	if len(failures) > 0 {
		st.Warnings = failures
	}
	st.ReviewPRs = nonNil(reviewItems)
	st.MyPRs = nonNil(myItems)
	st.AssignedIssues = nonNil(issueItems)
	st.ReviewPRsCount = reviewTotal
	st.MyPRsCount = myTotal
	st.AssignedIssuesCount = issueTotal
	st.Events = nonNil(events)
	if len(repos) > 30 {
		st.Repos = repos[:30]
	} else {
		st.Repos = nonNil(repos)
	}
	if len(failures) > 0 {
		log.Printf("账号 %s 详情部分失败: %v", acc.Username, failures)
	}

	st.FollowersCount = user.Followers
	st.FollowingCount = user.Following
	st.PublicRepos = user.PublicRepos
	st.TotalStars = 0
	for _, r := range repos {
		st.TotalStars += r.StargazersCount
	}
	st.TopRepos = topRepos(repos, 10)
	st.StarDistribution = starDistribution(repos)

	if len(followers) > 50 {
		st.FollowerList = followers[:50]
	} else {
		st.FollowerList = nonNil(followers)
	}
	if len(following) > 50 {
		st.FollowingList = following[:50]
	} else {
		st.FollowingList = nonNil(following)
	}

	// 速率限制（失败不影响主流程）
	if rl, err := c.RateLimit(); err == nil {
		st.RateLimit = rl
	}

	// 记录历史快照
	if s.Store != nil {
		_, _ = s.Store.SaveSnapshot(acc.Username, store.Metrics{
			Followers:      st.FollowersCount,
			Following:      st.FollowingCount,
			TotalStars:     st.TotalStars,
			PublicRepos:    st.PublicRepos,
			ReviewPRs:      len(reviewItems),
			MyPRs:          len(myItems),
			AssignedIssues: len(issueItems),
			Events:         len(events),
		}, forceRecord)
	}
	return st
}

// Summaries 并发拉取全部账号的汇总（带并发限流）。
func (s *Service) Summaries(accounts []config.Account) []*Status {
	sem := make(chan struct{}, maxConcurrentAccounts)
	var wg sync.WaitGroup
	out := make([]*Status, len(accounts))
	for i := range accounts {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = s.Summary(accounts[i])
		}(i)
	}
	wg.Wait()
	return out
}

func topRepos(repos []github.Repo, n int) []TopRepo {
	sorted := make([]github.Repo, len(repos))
	copy(sorted, repos)
	// 简单插入排序（数据量小）
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j].StargazersCount > sorted[j-1].StargazersCount; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	out := []TopRepo{}
	for _, r := range sorted {
		if r.StargazersCount <= 0 {
			continue
		}
		out = append(out, TopRepo{Name: r.Name, Stars: r.StargazersCount, URL: r.HTMLURL, Lang: r.Language})
		if len(out) >= n {
			break
		}
	}
	return out
}

func starDistribution(repos []github.Repo) []DistItem {
	ranges := []struct {
		label string
		low   int
		high  int // -1 表示无上限
	}{
		{"0 星", 0, 0},
		{"1-10", 1, 10},
		{"11-50", 11, 50},
		{"51-100", 51, 100},
		{"100+", 101, -1},
	}
	counts := make([]int, len(ranges))
	for _, r := range repos {
		for i, rg := range ranges {
			if r.StargazersCount >= rg.low && (rg.high < 0 || r.StargazersCount <= rg.high) {
				counts[i]++
				break
			}
		}
	}
	out := make([]DistItem, 0, len(ranges))
	for i, rg := range ranges {
		out = append(out, DistItem{Label: rg.label, Count: counts[i]})
	}
	return out
}

// nonNil 保证切片序列化为 [] 而不是 null。
func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// ------------------------------ 全局搜索 ------------------------------

// SearchResult 是一条全局搜索结果。
type SearchResult struct {
	AccountID    int      `json:"account_id"`
	AccountLabel string   `json:"account_label"`
	Title        string   `json:"title"`
	HTMLURL      string   `json:"html_url"`
	Repository   string   `json:"repository"`
	Number       int      `json:"number"`
	CreatedAt    string   `json:"created_at"`
	Labels       []string `json:"labels"`
}

func repoShort(url string) string {
	parts := strings.Split(strings.Trim(url, "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return url
}

// SearchAccount 在单个账号内按关键字搜索 PR / Issue。
func SearchAccount(acc config.Account, query, kind string) []SearchResult {
	if acc.Token == "" || strings.TrimSpace(query) == "" {
		return []SearchResult{}
	}
	c := github.New(acc.Token)
	user, err := c.User()
	if err != nil {
		return []SearchResult{}
	}
	login := user.Login

	var queries []string
	if kind == "all" || kind == "pr" {
		queries = append(queries, "is:open is:pr author:"+login+" "+query)
		queries = append(queries, "is:open is:pr review-requested:"+login+" "+query)
	}
	if kind == "all" || kind == "issue" {
		queries = append(queries, "is:open is:issue assignee:"+login+" "+query)
	}

	var mu sync.Mutex
	var results [][]github.IssueItem
	var wg sync.WaitGroup
	for _, q := range queries {
		wg.Add(1)
		go func(q string) {
			defer wg.Done()
			_, items, err := c.Search(q, 30)
			if err != nil {
				return
			}
			mu.Lock()
			results = append(results, items)
			mu.Unlock()
		}(q)
	}
	wg.Wait()

	out := []SearchResult{}
	seen := map[[2]any]bool{}
	for _, items := range results {
		for _, it := range items {
			key := [2]any{it.RepositoryURL, it.Number}
			if seen[key] {
				continue
			}
			seen[key] = true
			labels := make([]string, 0, len(it.Labels))
			for _, l := range it.Labels {
				labels = append(labels, l.Name)
			}
			out = append(out, SearchResult{
				AccountID:    acc.ID,
				AccountLabel: acc.Label,
				Title:        it.Title,
				HTMLURL:      it.HTMLURL,
				Repository:   repoShort(it.RepositoryURL),
				Number:       it.Number,
				CreatedAt:    it.CreatedAt,
				Labels:       labels,
			})
		}
	}
	return out
}

// GlobalSearch 跨账号搜索：每个账号用自己的 token 独立查询后合并去重。
func GlobalSearch(accounts []config.Account, query, kind string) map[string]any {
	if strings.TrimSpace(query) == "" {
		return map[string]any{"results": []SearchResult{}, "total": 0, "accounts": 0}
	}
	var mu sync.Mutex
	var all []SearchResult
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	for _, acc := range accounts {
		wg.Add(1)
		go func(acc config.Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			items := SearchAccount(acc, query, kind)
			mu.Lock()
			all = append(all, items...)
			mu.Unlock()
		}(acc)
	}
	wg.Wait()

	sort.SliceStable(all, func(i, j int) bool { return all[i].CreatedAt > all[j].CreatedAt })
	total := len(all)
	if len(all) > 100 {
		all = all[:100]
	}
	return map[string]any{"results": all, "total": total, "accounts": len(accounts)}
}
