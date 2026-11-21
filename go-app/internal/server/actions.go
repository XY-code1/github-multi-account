// 写操作 HTTP 处理器：以指定账号 Star 仓库、关注用户、创建 Pull Request。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github-multi-account/internal/config"
	"github-multi-account/internal/github"
	"github-multi-account/internal/ops"
)

// findAccount 按 ID 查账号。
func (s *Server) findAccount(id int) (config.Account, bool) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		return config.Account{}, false
	}
	for _, a := range accounts {
		if a.ID == id {
			return a, true
		}
	}
	return config.Account{}, false
}

// writeActionError 把 GitHub 错误映射成 HTTP 状态码，保持与 Python 版一致。
func writeActionError(w http.ResponseWriter, err error) {
	var ghErr *github.Error
	if errors.As(err, &ghErr) {
		status := ghErr.Status
		if status == 0 {
			// 0 表示本地参数/校验错误（如 repo 格式不正确）
			status = http.StatusBadRequest
		}
		switch status {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
			http.StatusNotFound, http.StatusUnprocessableEntity:
			writeJSON(w, status, map[string]any{"detail": ghErr.Error()})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"detail": ghErr.Error()})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
}

// handleActionStarRepo: POST /api/actions/star_repo
func (s *Server) handleActionStarRepo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountID int    `json:"accountId"`
		Owner     string `json:"owner"`
		Repo      string `json:"repo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	acc, ok := s.findAccount(in.AccountID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
		return
	}
	owner := strings.TrimSpace(in.Owner)
	repo := strings.TrimSpace(in.Repo)
	if owner == "" || repo == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "owner 与 repo 不能为空"})
		return
	}
	result, err := github.StarRepo(github.AccountRef{
		ID: acc.ID, Username: acc.Username, Token: acc.Token,
	}, owner, repo)
	if err != nil {
		writeActionError(w, err)
		return
	}
	ops.Audit(s.opsDB, "github.star", &acc.ID, acc.Username, "star "+owner+"/"+repo)
	writeJSON(w, http.StatusOK, result)
}

// handleActionFollowUser: POST /api/actions/follow_user
func (s *Server) handleActionFollowUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountID int    `json:"accountId"`
		Username  string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	acc, ok := s.findAccount(in.AccountID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
		return
	}
	login := strings.TrimSpace(in.Username)
	if login == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "username 不能为空"})
		return
	}
	result, err := github.FollowUser(github.AccountRef{
		ID: acc.ID, Username: acc.Username, Token: acc.Token,
	}, login)
	if err != nil {
		writeActionError(w, err)
		return
	}
	ops.Audit(s.opsDB, "github.follow", &acc.ID, acc.Username, "关注 "+login)
	writeJSON(w, http.StatusOK, result)
}

// handleActionCreatePR: POST /api/actions/create_pr
func (s *Server) handleActionCreatePR(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountID   int    `json:"accountId"`
		Repo        string `json:"repo"`
		Title       string `json:"title"`
		Base        string `json:"base"`
		Head        string `json:"head"`
		Body        string `json:"body"`
		Draft       bool   `json:"draft"`
		Fork        bool   `json:"fork"`
		Branch      string `json:"branch"`
		FilePath    string `json:"filePath"`
		FileContent string `json:"fileContent"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	acc, ok := s.findAccount(in.AccountID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	result, err := github.CreatePR(ctx, github.AccountRef{
		ID: acc.ID, Username: acc.Username, Token: acc.Token,
	}, strings.TrimSpace(in.Repo), strings.TrimSpace(in.Title), github.CreatePROptions{
		Base:     strings.TrimSpace(in.Base),
		Head:     strings.TrimSpace(in.Head),
		Body:     in.Body,
		Draft:    in.Draft,
		Fork:     in.Fork,
		Branch:   strings.TrimSpace(in.Branch),
		FilePath: strings.TrimSpace(in.FilePath),
		Content:  in.FileContent,
	})
	if err != nil {
		writeActionError(w, err)
		return
	}
	ops.Audit(s.opsDB, "github.create_pr", &acc.ID, acc.Username,
		"创建 PR："+strings.TrimSpace(in.Repo)+" → "+result["action"].(string))
	writeJSON(w, http.StatusOK, result)
}
