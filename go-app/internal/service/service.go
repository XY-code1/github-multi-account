// Package service 聚合 GitHub 数据：登录态、PR、Issue、动态、仓库、粉丝。
//
// 容错原则：并发请求中任意一个失败，只降级该字段，不拖垮整个账号。
package service

import (
	"errors"
	"log"
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
	return &Status{
		ID:               acc.ID,
		Label:            acc.Label,
		Username:         acc.Username,
		TokenMasked:      acc.MaskedToken(),
		Status:           "unknown",
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
	if len(failures) > 0 {
		log.Printf("账号 %s 汇总部分失败: %v", acc.Username, failures)
	}
	return st
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
