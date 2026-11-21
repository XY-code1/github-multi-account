// 写操作用例：Star 仓库、关注用户、创建 Pull Request。
//
// 约定与 Python 版 app/actions.py 保持一致：
//   - 写操作前先做配额预检（core 剩余 ≥ QuotaReserve）；
//   - 幂等：star / follow 重复执行无副作用，CreatePR 先查已有同源同目标的开 PR；
//   - 返回 map，字段与 Python 版对齐，便于双版本行为一致。
package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// QuotaReserve 是写操作前要求 core 配额至少剩余的次数。
const QuotaReserve = 50

// AccountRef 是一次写操作使用的账号身份。
type AccountRef struct {
	ID       int
	Username string
	Token    string
}

// RateLimit 是 /rate_limit 的 core 配额。
type RateLimit struct {
	Limit     int   `json:"limit"`
	Remaining int   `json:"remaining"`
	Reset     int64 `json:"reset"`
}

// PullRequest 是创建 PR 时需要的关键字段。
type PullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	State   string `json:"state"`
}

type repoInfo struct {
	DefaultBranch string `json:"default_branch"`
	Fork          bool   `json:"fork"`
}

// CheckQuota 做写操作前的配额预检，低于 minRemaining 时返回错误。
func (c *Client) CheckQuota(minRemaining int) (*RateLimit, error) {
	var out struct {
		Resources struct {
			Core RateLimit `json:"core"`
		} `json:"resources"`
	}
	if err := c.get("/rate_limit", nil, &out); err != nil {
		return nil, err
	}
	core := out.Resources.Core
	if core.Remaining < minRemaining {
		return &core, &Error{403, fmt.Sprintf(
			"API 配额不足：core 剩余 %d 次，写操作要求 ≥ %d", core.Remaining, minRemaining)}
	}
	return &core, nil
}

// IsStarred 报告当前 token 用户是否已 Star 该仓库。
func (c *Client) IsStarred(owner, repo string) (bool, error) {
	status, _, err := c.getStatus("/user/starred/" + Path(owner, repo))
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusNoContent:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, &Error{status, fmt.Sprintf("检查 Star 状态失败 (HTTP %d)", status)}
}

// StarRepo star 仓库（幂等），返回是否原本已 star。
func (c *Client) StarRepo(owner, repo string) (bool, error) {
	if _, err := c.CheckQuota(QuotaReserve); err != nil {
		return false, err
	}
	already, err := c.IsStarred(owner, repo)
	if err != nil {
		return false, err
	}
	if already {
		return true, nil
	}
	if err := c.sendJSON(http.MethodPut, "/user/starred/"+Path(owner, repo), nil, nil,
		http.StatusNoContent); err != nil {
		return false, err
	}
	return false, nil
}

// UnstarRepo 取消 star（幂等）。
func (c *Client) UnstarRepo(owner, repo string) (bool, error) {
	if _, err := c.CheckQuota(QuotaReserve); err != nil {
		return false, err
	}
	already, err := c.IsStarred(owner, repo)
	if err != nil {
		return false, err
	}
	if !already {
		return true, nil
	}
	if err := c.sendJSON(http.MethodDelete, "/user/starred/"+Path(owner, repo), nil, nil,
		http.StatusNoContent); err != nil {
		return false, err
	}
	return false, nil
}

// IsFollowing 报告当前 token 用户是否已关注该用户。
func (c *Client) IsFollowing(login string) (bool, error) {
	status, _, err := c.getStatus("/user/following/" + url.PathEscape(login))
	if err != nil {
		return false, err
	}
	switch status {
	case http.StatusNoContent:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	}
	return false, &Error{status, fmt.Sprintf("检查关注状态失败 (HTTP %d)", status)}
}

// Follow 关注用户（幂等），返回是否原本已关注。
func (c *Client) Follow(login string) (bool, error) {
	if _, err := c.CheckQuota(QuotaReserve); err != nil {
		return false, err
	}
	already, err := c.IsFollowing(login)
	if err != nil {
		return false, err
	}
	if already {
		return true, nil
	}
	if err := c.sendJSON(http.MethodPut, "/user/following/"+url.PathEscape(login), nil, nil,
		http.StatusNoContent); err != nil {
		return false, err
	}
	return false, nil
}

// Unfollow 取消关注（幂等）。
func (c *Client) Unfollow(login string) (bool, error) {
	if _, err := c.CheckQuota(QuotaReserve); err != nil {
		return false, err
	}
	already, err := c.IsFollowing(login)
	if err != nil {
		return false, err
	}
	if !already {
		return true, nil
	}
	if err := c.sendJSON(http.MethodDelete, "/user/following/"+url.PathEscape(login), nil, nil,
		http.StatusNoContent); err != nil {
		return false, err
	}
	return false, nil
}

// RepoInfo 返回仓库的默认分支与是否为 fork。
func (c *Client) RepoInfo(owner, repo string) (*repoInfo, error) {
	var out repoInfo
	if err := c.get("/repos/"+Path(owner, repo), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// EnsureFork 确保 login 名下存在 owner/repo 的 fork；没有就创建并等待就绪。
func (c *Client) EnsureFork(ctx context.Context, owner, repo, login string, timeout time.Duration) (bool, error) {
	status, raw, err := c.getStatus("/repos/" + Path(login, repo))
	if err != nil {
		return false, err
	}
	if status == http.StatusOK {
		var info repoInfo
		if jsonUnmarshal(raw, &info) == nil && info.Fork {
			return false, nil
		}
	}
	if err := c.sendJSON(http.MethodPost, "/repos/"+Path(owner, repo)+"/forks", nil, nil,
		http.StatusOK, http.StatusAccepted); err != nil {
		return false, err
	}
	deadline := time.Now().Add(timeout)
	for {
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		status, raw, err := c.getStatus("/repos/" + Path(login, repo))
		if err != nil {
			return false, err
		}
		if status == http.StatusOK {
			var info repoInfo
			if jsonUnmarshal(raw, &info) == nil && info.Fork {
				return true, nil
			}
		}
		if time.Now().After(deadline) {
			return false, &Error{0, fmt.Sprintf("fork %s/%s 等待超时，请稍后重试", owner, repo)}
		}
	}
}

// CreateBranch 基于 fromBranch 创建分支；已存在时视为成功。
func (c *Client) CreateBranch(owner, repo, branch, fromBranch string) (bool, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.get("/repos/"+Path(owner, repo)+"/git/ref/heads/"+url.PathEscape(fromBranch), nil, &ref); err != nil {
		return false, err
	}
	if ref.Object.SHA == "" {
		return false, &Error{0, fmt.Sprintf("无法获取 %s/%s 分支 %s 的 SHA", owner, repo, fromBranch)}
	}
	_, status, _, err := c.send(http.MethodPost, "/repos/"+Path(owner, repo)+"/git/refs",
		map[string]any{"ref": "refs/heads/" + branch, "sha": ref.Object.SHA})
	if err != nil {
		return false, err
	}
	if status == http.StatusCreated || status == http.StatusOK {
		return true, nil
	}
	// 422 通常表示分支已存在，幂等处理
	return false, nil
}

// PutFile 在指定分支创建/更新文本文件。
func (c *Client) PutFile(owner, repo, path, content, branch, message string) error {
	clean := strings.Trim(path, "/")
	if clean == "" {
		return &Error{0, "文件路径不能为空"}
	}
	escaped := escapePath(clean)
	status, raw, err := c.getStatus("/repos/" + Path(owner, repo) + "/contents/" + escaped + "?ref=" + url.QueryEscape(branch))
	if err != nil {
		return err
	}
	payload := map[string]any{
		"message": message,
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"branch":  branch,
	}
	if status == http.StatusOK {
		var existing struct {
			SHA string `json:"sha"`
		}
		if jsonUnmarshal(raw, &existing) == nil && existing.SHA != "" {
			payload["sha"] = existing.SHA
		}
	} else if status != http.StatusNotFound {
		return &Error{status, fmt.Sprintf("读取文件 %s 失败 (HTTP %d)", path, status)}
	}
	return c.sendJSON(http.MethodPut,
		"/repos/"+Path(owner, repo)+"/contents/"+escaped,
		payload, nil, http.StatusOK, http.StatusCreated)
}

// escapePath 逐段转义文件路径，保留层级结构。
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// FindOpenPR 查找同源同目标的开 PR（幂等创建用）。
func (c *Client) FindOpenPR(owner, repo, head, base string) (*PullRequest, error) {
	params := url.Values{}
	params.Set("state", "open")
	params.Set("head", head)
	params.Set("per_page", "1")
	if base != "" {
		params.Set("base", base)
	}
	var items []PullRequest
	if err := c.get("/repos/"+Path(owner, repo)+"/pulls", params, &items); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	return &items[0], nil
}

// CreatePROptions 描述一次创建 PR 的参数。
type CreatePROptions struct {
	Title    string
	Base     string // 为空则用目标仓库默认分支
	Head     string // 分支名（fork 模式下为 fork 内分支名）
	Body     string
	Draft    bool
	Fork     bool
	Branch   string // 需要提交文件时的目标分支
	FilePath string
	Content  string
}

// StarRepo 以指定账号 star 仓库。
func StarRepo(acc AccountRef, owner, repo string) (map[string]any, error) {
	c := New(acc.Token)
	already, err := c.StarRepo(owner, repo)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "account": acc.Username, "owner": owner, "repo": repo,
		"starred": true, "already": already,
	}, nil
}

// FollowUser 以指定账号关注用户。
func FollowUser(acc AccountRef, login string) (map[string]any, error) {
	c := New(acc.Token)
	already, err := c.Follow(login)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "account": acc.Username, "username": login,
		"following": true, "already": already,
	}, nil
}

// CreatePR 以指定账号创建 Pull Request。
//
// fork=false 时 head 为同仓库分支名（需账号有写权限）；
// fork=true 时自动 fork（如尚未 fork），head 为 login:branch。
func CreatePR(ctx context.Context, acc AccountRef, repo, title string, opts CreatePROptions) (map[string]any, error) {
	owner, name, err := SplitRepo(repo)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(title) == "" {
		return nil, &Error{0, "title 不能为空"}
	}
	if (opts.FilePath == "") != (opts.Content == "") {
		return nil, &Error{0, "file_path 与 file_content 必须同时提供"}
	}

	c := New(acc.Token)
	var me User
	if err := c.get("/user", nil, &me); err != nil {
		return nil, err
	}
	login := me.Login
	if login == "" {
		login = acc.Username
	}
	if _, err := c.CheckQuota(QuotaReserve); err != nil {
		return nil, err
	}
	info, err := c.RepoInfo(owner, name)
	if err != nil {
		return nil, err
	}
	baseBranch := opts.Base
	if baseBranch == "" {
		baseBranch = info.DefaultBranch
	}
	if baseBranch == "" {
		baseBranch = "main"
	}
	branch := opts.Branch
	if branch == "" {
		branch = opts.Head
	}
	if branch == "" {
		return nil, &Error{0, "必须提供 head（分支名）或 branch"}
	}

	workOwner := owner
	headRef := branch
	if opts.Fork {
		if _, err := c.EnsureFork(ctx, owner, name, login, 60*time.Second); err != nil {
			return nil, err
		}
		workOwner = login
		headRef = login + ":" + branch
	}

	if opts.FilePath != "" {
		if opts.Branch == "" {
			return nil, &Error{0, "提供文件时 branch 为必填"}
		}
		if _, err := c.CreateBranch(workOwner, name, opts.Branch, baseBranch); err != nil {
			return nil, err
		}
		if err := c.PutFile(workOwner, name, opts.FilePath, opts.Content, opts.Branch, title+"（自动提交）"); err != nil {
			return nil, err
		}
	}

	existing, err := c.FindOpenPR(owner, name, headRef, baseBranch)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return map[string]any{
			"ok": true, "action": "existing", "account": login,
			"repo": owner + "/" + name, "pr": existing, "url": existing.HTMLURL,
		}, nil
	}

	payload := map[string]any{
		"title": title, "head": headRef, "base": baseBranch, "draft": opts.Draft,
	}
	if opts.Body != "" {
		payload["body"] = opts.Body
	}
	var pr PullRequest
	if err := c.sendJSON(http.MethodPost, "/repos/"+Path(owner, name)+"/pulls", payload, &pr,
		http.StatusOK, http.StatusCreated); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok": true, "action": "created", "account": login,
		"repo": owner + "/" + name, "pr": pr, "url": pr.HTMLURL,
	}, nil
}

// SplitRepo 解析 "owner/repo"。
func SplitRepo(repo string) (string, string, error) {
	parts := strings.Split(strings.Trim(strings.TrimSpace(repo), "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", &Error{0, "repo 格式不正确，应为 owner/repo"}
	}
	return parts[0], parts[1], nil
}

// Path 拼接并转义 URL 路径段。
func Path(parts ...string) string {
	escaped := make([]string, 0, len(parts))
	for _, p := range parts {
		escaped = append(escaped, url.PathEscape(p))
	}
	return strings.Join(escaped, "/")
}

func jsonUnmarshal(raw []byte, out any) error {
	return json.Unmarshal(raw, out)
}
