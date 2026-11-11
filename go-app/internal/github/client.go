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

func (c *Client) get(path string, params url.Values, out any) error {
	u := BaseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "github-account-dashboard")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return &Error{401, "Token 无效或已过期"}
	case http.StatusForbidden:
		msg := "API 速率限制或权限不足"
		var m map[string]any
		if json.Unmarshal(body, &m) == nil {
			if s, ok := m["message"].(string); ok && s != "" {
				msg = s
			}
		}
		return &Error{403, msg}
	case http.StatusNotFound:
		return &Error{404, "资源不存在或无访问权限"}
	}
	if resp.StatusCode >= 400 {
		return &Error{resp.StatusCode, fmt.Sprintf("GitHub API 错误 (HTTP %d)", resp.StatusCode)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
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

type IssueItem struct {
	Title         string  `json:"title"`
	HTMLURL       string  `json:"html_url"`
	RepositoryURL string  `json:"repository_url"`
	Number        int     `json:"number"`
	User          User    `json:"user"`
	Labels        []Label `json:"labels"`
	CreatedAt     string  `json:"created_at"`
}

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
