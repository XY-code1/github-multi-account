// Package github 是一个精简的 GitHub REST API 客户端。
package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// BaseURL 是 API 根地址，测试时可替换为 httptest 服务器。
var BaseURL = "https://api.github.com"

// ClientVersion 是客户端版本，与 Python 版 app/github.py 的 CLIENT_VERSION、
// server.Version 保持一致（改版本时三处同步）。
const ClientVersion = "1.3.0"

// UserAgent 是统一的请求标识：GitHub 建议可识别的 UA，双端一致，
// 便于被风控时定位来源。
const UserAgent = "github-multi-account/" + ClientVersion

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
	req, err := c.newRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, "", false, err
	}
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

// newRequest 构造一个带通用鉴权头的请求。u 为完整 URL。
func (c *Client) newRequest(method, u string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", UserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// send 发起写请求（POST/PUT/DELETE），返回响应体、状态码与响应头。
// 401/403/429 会被翻译成带配额提示的 *Error，其余状态码由调用方判断。
func (c *Client) send(method, path string, payload any) ([]byte, int, http.Header, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := c.newRequest(method, BaseURL+path, body)
	if err != nil {
		return nil, 0, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return nil, resp.StatusCode, resp.Header, &Error{401, "Token 无效或已过期"}
	case http.StatusForbidden, http.StatusTooManyRequests:
		msg := "API 速率限制或权限不足"
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			if s, ok := m["message"].(string); ok && s != "" {
				msg = s
			}
		}
		if hint := rateLimitHint(resp.Header); hint != "" {
			msg += "（" + hint + "）"
		}
		return nil, resp.StatusCode, resp.Header, &Error{resp.StatusCode, msg}
	}
	return raw, resp.StatusCode, resp.Header, nil
}

// sendJSON 发起写请求并按 okCodes 校验状态码，成功时把响应解析进 out。
func (c *Client) sendJSON(method, path string, payload, out any, okCodes ...int) error {
	raw, status, _, err := c.send(method, path, payload)
	if err != nil {
		return err
	}
	if !containsInt(okCodes, status) {
		return &Error{status, fmt.Sprintf("GitHub API 错误 (HTTP %d)", status)}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// getStatus 发起 GET 并返回状态码（404 等不抛错，供幂等判断使用）。
func (c *Client) getStatus(path string) (int, []byte, error) {
	req, err := c.newRequest(http.MethodGet, BaseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return resp.StatusCode, nil, &Error{401, "Token 无效或已过期"}
	case http.StatusForbidden, http.StatusTooManyRequests:
		msg := "API 速率限制或权限不足"
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			if s, ok := m["message"].(string); ok && s != "" {
				msg = s
			}
		}
		if hint := rateLimitHint(resp.Header); hint != "" {
			msg += "（" + hint + "）"
		}
		return resp.StatusCode, nil, &Error{resp.StatusCode, msg}
	}
	return resp.StatusCode, raw, nil
}

func containsInt(list []int, v int) bool {
	for _, n := range list {
		if n == v {
			return true
		}
	}
	return false
}

// rateLimitHint 从响应头提取配额剩余与可重试等待时间。
func rateLimitHint(h http.Header) string {
	var parts []string
	if remaining := h.Get("X-RateLimit-Remaining"); remaining != "" {
		parts = append(parts, "剩余 "+remaining+" 次")
	}
	if wait := retryAfterSeconds(h); wait > 0 {
		parts = append(parts, fmt.Sprintf("约 %d 秒后可重试", int(wait)))
	}
	return strings.Join(parts, "，")
}

// retryAfterSeconds 优先用 Retry-After，其次用 X-RateLimit-Reset 计算。
func retryAfterSeconds(h http.Header) float64 {
	if raw := h.Get("Retry-After"); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
	}
	if raw := h.Get("X-RateLimit-Reset"); raw != "" {
		if reset, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return math.Max(0, float64(reset-time.Now().Unix()))
		}
	}
	return 0
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
