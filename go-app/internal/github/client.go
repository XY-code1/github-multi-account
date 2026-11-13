// Package github 是一个精简的 GitHub REST API 客户端。
package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// BaseURL 是 API 根地址，测试时可替换为 httptest 服务器。
var BaseURL = "https://api.github.com"

// Error 表示 GitHub API 返回的错误。
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// Client 是 GitHub API 客户端。
type Client struct {
	token string
	http  *http.Client
}

func New(token string) *Client {
	return &Client{token: token, http: &http.Client{Timeout: 12 * time.Second}}
}

// getRaw 返回 (响应体, ETag, 是否有变化)。
// 带上 If-None-Match 后未变化时 GitHub 返回 304 且不计入 API 配额。
func (c *Client) getRaw(path string, params url.Values, etag string) ([]byte, string, bool, error) {
	u := BaseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "github-account-dashboard")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()

	newETag := resp.Header.Get("ETag")
	if resp.StatusCode == http.StatusNotModified {
		return nil, newETag, false, nil
	}
	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, "", false, &Error{401, "Token 无效或已过期"}
	case http.StatusForbidden:
		msg := "API 速率限制或权限不足"
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			if s, ok := m["message"].(string); ok && s != "" {
				msg = s
			}
		}
		return nil, "", false, &Error{403, msg}
	case http.StatusNotFound:
		return nil, "", false, &Error{404, "资源不存在或无访问权限"}
	}
	if resp.StatusCode >= 400 {
		return nil, "", false, &Error{resp.StatusCode, fmt.Sprintf("GitHub API 错误 (HTTP %d)", resp.StatusCode)}
	}
	return body, newETag, true, nil
}

func (c *Client) get(path string, params url.Values, out any) error {
	body, _, _, err := c.getRaw(path, params, "")
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// SearchCond 是带 ETag 的搜索：返回 (总数, 条目, 新 ETag, 是否有变化)。
func (c *Client) SearchCond(query string, perPage int, etag string) (int, []IssueItem, string, bool, error) {
	params := url.Values{}
	params.Set("q", query)
	params.Set("per_page", strconv.Itoa(perPage))
	body, newETag, modified, err := c.getRaw("/search/issues", params, etag)
	if err != nil {
		return 0, nil, newETag, false, err
	}
	if !modified {
		return 0, nil, newETag, false, nil
	}
	var out struct {
		TotalCount int         `json:"total_count"`
		Items      []IssueItem `json:"items"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, nil, newETag, false, err
	}
	return out.TotalCount, out.Items, newETag, true, nil
}

// ReceivedEventsCond 是带 ETag 的动态拉取：返回 (动态, 新 ETag, 是否有变化)。
func (c *Client) ReceivedEventsCond(login string, perPage int, etag string) ([]Event, string, bool, error) {
	params := url.Values{}
	params.Set("per_page", strconv.Itoa(perPage))
	body, newETag, modified, err := c.getRaw(
		"/users/"+url.PathEscape(login)+"/received_events", params, etag)
	if err != nil {
		return nil, newETag, false, err
	}
	if !modified {
		return nil, newETag, false, nil
	}
	var out []Event
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, newETag, false, err
	}
	return out, newETag, true, nil
}

// ---------- 用户 ----------

type User struct {
	Login       string `json:"login"`
	Name        string `json:"name"`
	AvatarURL   string `json:"avatar_url"`
	Followers   int    `json:"followers"`
	Following   int    `json:"following"`
	PublicRepos int    `json:"public_repos"`
}

func (c *Client) User() (*User, error) {
	var u User
	if err := c.get("/user", nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// ---------- 搜索 ----------

type Label struct {
	Name string `json:"name"`
}

// PullRequestRef 用于区分搜索结果里的 PR 与 Issue。
type PullRequestRef struct {
	URL string `json:"url"`
}

type IssueItem struct {
	Title         string         `json:"title"`
	HTMLURL       string         `json:"html_url"`
	RepositoryURL string         `json:"repository_url"`
	Number        int            `json:"number"`
	User          User           `json:"user"`
	Labels        []Label        `json:"labels"`
	CreatedAt     string         `json:"created_at"`
	State         string         `json:"state"`
	UpdatedAt     string         `json:"updated_at"`
	PullRequest   *PullRequestRef `json:"pull_request"`
}

// IsPullRequest 报告该条目是否为 PR（GitHub 搜索结果里 PR 带 pull_request 字段）。
func (i IssueItem) IsPullRequest() bool { return i.PullRequest != nil }

// Search 返回 (真实总数, 当前页条目)。
func (c *Client) Search(query string, perPage int) (int, []IssueItem, error) {
	params := url.Values{}
	params.Set("q", query)
	params.Set("per_page", strconv.Itoa(perPage))
	var out struct {
		TotalCount int         `json:"total_count"`
		Items      []IssueItem `json:"items"`
	}
	if err := c.get("/search/issues", params, &out); err != nil {
		return 0, nil, err
	}
	return out.TotalCount, out.Items, nil
}

func (c *Client) ReviewRequestedPRs(login string, perPage int) (int, []IssueItem, error) {
	return c.Search("is:open is:pr review-requested:"+login, perPage)
}

func (c *Client) MyOpenPRs(login string, perPage int) (int, []IssueItem, error) {
	return c.Search("is:open is:pr author:"+login, perPage)
}

func (c *Client) AssignedIssues(login string, perPage int) (int, []IssueItem, error) {
	return c.Search("is:open is:issue assignee:"+login, perPage)
}

// ---------- 动态与仓库 ----------

type EventActor struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
}

type EventRepo struct {
	Name string `json:"name"`
}

type Event struct {
	Type      string         `json:"type"`
	Actor     EventActor     `json:"actor"`
	Repo      EventRepo      `json:"repo"`
	Payload   map[string]any `json:"payload"`
	CreatedAt string         `json:"created_at"`
}

func (c *Client) ReceivedEvents(login string, perPage int) ([]Event, error) {
	params := url.Values{}
	params.Set("per_page", strconv.Itoa(perPage))
	var out []Event
	if err := c.get("/users/"+url.PathEscape(login)+"/received_events", params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

type Repo struct {
	Name            string `json:"name"`
	HTMLURL         string `json:"html_url"`
	StargazersCount int    `json:"stargazers_count"`
	ForksCount      int    `json:"forks_count"`
	Language        string `json:"language"`
	Private         bool   `json:"private"`
	UpdatedAt       string `json:"updated_at"`
}

func (c *Client) userRepoPage(login string, page, perPage int) ([]Repo, error) {
	params := url.Values{}
	params.Set("sort", "updated")
	params.Set("direction", "desc")
	params.Set("per_page", strconv.Itoa(perPage))
	params.Set("page", strconv.Itoa(page))
	var out []Repo
	if err := c.get("/users/"+url.PathEscape(login)+"/repos", params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// UserReposAll 并发分页拉取公开仓库。
func (c *Client) UserReposAll(login string, maxPages, concurrency int) ([]Repo, error) {
	if concurrency < 1 {
		concurrency = 5
	}
	var all []Repo
	page := 1
	for page <= maxPages {
		end := page + concurrency - 1
		if end > maxPages {
			end = maxPages
		}
		type result struct {
			repos []Repo
			err   error
		}
		ch := make(chan result, end-page+1)
		for p := page; p <= end; p++ {
			go func(p int) {
				r, err := c.userRepoPage(login, p, 100)
				ch <- result{r, err}
			}(p)
		}
		stop := false
		for i := 0; i < end-page+1; i++ {
			r := <-ch
			if r.err != nil {
				stop = true
				continue
			}
			all = append(all, r.repos...)
			if len(r.repos) < 100 {
				stop = true
			}
		}
		if stop {
			break
		}
		page = end + 1
	}
	return all, nil
}

type Person struct {
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
}

func (c *Client) Followers(login string, perPage int) ([]Person, error) {
	params := url.Values{}
	params.Set("per_page", strconv.Itoa(perPage))
	var out []Person
	if err := c.get("/users/"+url.PathEscape(login)+"/followers", params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Following(login string, perPage int) ([]Person, error) {
	params := url.Values{}
	params.Set("per_page", strconv.Itoa(perPage))
	var out []Person
	if err := c.get("/users/"+url.PathEscape(login)+"/following", params, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) RateLimit() (map[string]any, error) {
	var out map[string]any
	if err := c.get("/rate_limit", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
