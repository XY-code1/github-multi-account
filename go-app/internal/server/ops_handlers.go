// 运维中心 HTTP 处理器：全局聚合、分页账号列表、同步引擎控制、审计日志。
package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github-multi-account/internal/config"
	"github-multi-account/internal/notify"
	"github-multi-account/internal/ops"
)

// SetOpsDB 设置运维中心数据库（与快照同一个库）。
func (s *Server) SetOpsDB(db *sql.DB) { s.opsDB = db }

// SetEngine 设置后台同步引擎。
func (s *Server) SetEngine(e *ops.Engine) { s.engine = e }

// SetConfigDir 设置配置目录（同步设置持久化位置）。
func (s *Server) SetConfigDir(dir string) { s.cfgDir = dir }

func (s *Server) handleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	summary, err := ops.BuildSummary(s.opsDB, accounts)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleDashboardTasks(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	all, err := ops.BuildTasks(s.opsDB, accounts)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	q := r.URL.Query()
	priority := q.Get("priority")
	typeFilter := q.Get("type")
	accountFilter := q.Get("accountId")

	tasks := []ops.Task{}
	for _, t := range all {
		if priority != "" && t.Priority != priority {
			continue
		}
		if typeFilter != "" && t.Type != typeFilter {
			continue
		}
		if accountFilter != "" && strconv.Itoa(t.AccountID) != accountFilter {
			continue
		}
		tasks = append(tasks, t)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tasks":  tasks,
		"total":  len(tasks),
		"high":   countPriority(tasks, "high"),
		"medium": countPriority(tasks, "medium"),
		"low":    countPriority(tasks, "low"),
	})
}

func countPriority(tasks []ops.Task, p string) int {
	n := 0
	for _, t := range tasks {
		if t.Priority == p {
			n++
		}
	}
	return n
}

func (s *Server) handleDashboardActivities(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	typeFilter := r.URL.Query().Get("type")
	if typeFilter == "" {
		typeFilter = "all"
	}
	items, err := ops.BuildActivities(s.opsDB, limit, typeFilter)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"activities": items})
}

// handleAccounts 是分页账号列表（服务端搜索 / 筛选 / 排序）。
func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	q := r.URL.Query()
	sortBy := q.Get("sort")
	if sortBy == "" {
		sortBy = "last_activity"
	}
	order := q.Get("order")
	if order == "" {
		order = "desc"
	}
	page, _ := strconv.Atoi(q.Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ := strconv.Atoi(q.Get("pageSize"))
	if pageSize < 1 {
		pageSize = 50
	}
	result, err := ops.QueryAccounts(s.opsDB, accounts, ops.AccountQuery{
		Page:     page,
		PageSize: pageSize,
		Query:    q.Get("query"),
		Status:   q.Get("status"),
		Type:     q.Get("type"),
		Group:    q.Get("group"),
		Tags:     q.Get("tags"),
		Sort:     sortBy,
		Order:    order,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleGetGraph(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	graph, err := ops.BuildGraph(s.opsDB, accounts)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, graph)
}

// SyncGraph 慢速同步：拉取所有账号的粉丝 / 关注 / 代表仓库，返回账号数。
func (s *Server) SyncGraph() (int, error) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		return 0, err
	}
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, a := range accounts {
		wg.Add(1)
		go func(acc config.Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			followers, following, repos, err := s.svc.FetchSocial(acc)
			if err != nil {
				return
			}
			_ = ops.ReplaceSocial(s.opsDB, acc.ID, "followers", followers)
			_ = ops.ReplaceSocial(s.opsDB, acc.ID, "following", following)
			_ = ops.ReplaceRepos(s.opsDB, acc.ID, repos)
		}(a)
	}
	wg.Wait()
	ops.Audit(s.opsDB, "graph.sync", nil, "", fmt.Sprintf("关系图谱同步 %d 个账号", len(accounts)))
	return len(accounts), nil
}

// handleSyncGraph 慢速同步：拉取所有账号的粉丝 / 关注 / 代表仓库。
func (s *Server) handleSyncGraph(w http.ResponseWriter, r *http.Request) {
	total, err := s.SyncGraph()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "total": total})
}

func (s *Server) handleGetWebhooks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"webhooks": notify.LoadWebhooks(filepath.Join(s.baseDir, "config")),
		"types":    notify.WebhookEventTypes,
	})
}

func (s *Server) handleSaveWebhooks(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Webhooks []notify.Webhook `json:"webhooks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	dir := filepath.Join(s.baseDir, "config")
	if err := notify.SaveWebhooks(dir, in.Webhooks); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	ops.Audit(s.opsDB, "webhooks.settings", nil, "", fmt.Sprintf("更新出站 Webhook 配置：%d 条", len(in.Webhooks)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "webhooks": in.Webhooks})
}

func (s *Server) handleWebhooksLog(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	items, err := ops.ListWebhookLog(s.opsDB, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

// RebuildMeta 从当前账号配置重建元数据镜像（搜索 / 筛选用）。
func (s *Server) RebuildMeta() {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		return
	}
	metas := make([]ops.Meta, 0, len(accounts))
	for _, a := range accounts {
		metas = append(metas, ops.Meta{
			AccountID: a.ID,
			Username:  a.Username,
			Type:      a.Type,
			Tags:      a.Tags,
			Note:      a.Note,
			Alias:     a.Alias,
			GroupName: a.Group,
		})
	}
	if err := ops.RebuildMeta(s.opsDB, metas); err != nil {
		log.Printf("重建账号元数据失败: %v", err)
	}
}

func (s *Server) handleBatchRefresh(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []int `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	if len(in.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "ids 必须是非空列表"})
		return
	}
	ops.Audit(s.opsDB, "accounts.batch_refresh", nil, "", "批量刷新 "+strconv.Itoa(len(in.IDs))+" 个账号")
	result := s.engine.SyncAll(in.IDs)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "total": result.Total, "done": result.Done, "failed": result.Failed})
}

func (s *Server) handleBatchToggleSync(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs     []int `json:"ids"`
		Enabled bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	if len(in.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "ids 必须是非空列表"})
		return
	}
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	names := make(map[int]string, len(accounts))
	for _, a := range accounts {
		names[a.ID] = a.Username
	}
	for _, id := range in.IDs {
		_ = ops.SetSyncEnabled(s.opsDB, id, names[id], in.Enabled)
	}
	action := "批量暂停同步 "
	if in.Enabled {
		action = "批量启用同步 "
	}
	ops.Audit(s.opsDB, "accounts.batch_toggle_sync", nil, "", action+strconv.Itoa(len(in.IDs))+" 个账号")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": len(in.IDs)})
}

func (s *Server) handleBatchTag(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs  []int    `json:"ids"`
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	if len(in.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "ids 必须是非空列表"})
		return
	}
	clean := cleanTags(in.Tags)
	if len(clean) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "tags 必须是非空列表"})
		return
	}
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	want := make(map[int]bool, len(in.IDs))
	for _, id := range in.IDs {
		want[id] = true
	}
	changed := 0
	for i := range accounts {
		if !want[accounts[i].ID] {
			continue
		}
		merged := append([]string{}, accounts[i].Tags...)
		for _, t := range clean {
			if !containsStr(merged, t) {
				merged = append(merged, t)
			}
		}
		accounts[i].Tags = merged
		changed++
	}
	if changed > 0 {
		if err := s.cfg.Save(accounts); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		s.RebuildMeta()
	}
	ops.Audit(s.opsDB, "accounts.batch_tag", nil, "", "批量打标签 "+strconv.Itoa(changed)+" 个账号："+strings.Join(clean, ","))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": changed})
}

func (s *Server) handleBatchGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs   []int  `json:"ids"`
		Group string `json:"group"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	if len(in.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "ids 必须是非空列表"})
		return
	}
	group := strings.TrimSpace(in.Group)
	if group == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "group 不能为空"})
		return
	}
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	want := make(map[int]bool, len(in.IDs))
	for _, id := range in.IDs {
		want[id] = true
	}
	changed := 0
	for i := range accounts {
		if want[accounts[i].ID] {
			accounts[i].Group = group
			changed++
		}
	}
	if changed > 0 {
		if err := s.cfg.Save(accounts); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		s.RebuildMeta()
	}
	ops.Audit(s.opsDB, "accounts.batch_group", nil, "", "批量移动到分组「"+group+"」："+strconv.Itoa(changed)+" 个账号")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": changed})
}

func cleanTags(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range in {
		if v := strings.TrimSpace(t); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (s *Server) handleSyncAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
		return
	}
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	for _, a := range accounts {
		if a.ID == id {
			ok, status, reason := s.engine.SyncAccount(a)
			ops.Audit(s.opsDB, "accounts.sync", &a.ID, a.Username, "手动同步")
			out := map[string]any{"id": a.ID, "ok": ok, "status": status}
			if reason != "" {
				out["reason"] = reason
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
}

// handleTasks 返回同步引擎进度与配置。
func (s *Server) handleTasks(w http.ResponseWriter, r *http.Request) {
	states, err := ops.AllSyncStates(s.opsDB)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"progress": s.engine.Progress(),
		"config":   s.engine.Config(),
		"running":  s.engine.IsRunning(),
		"states":   states,
	})
}

func (s *Server) handleSyncSettings(w http.ResponseWriter, r *http.Request) {
	var in ops.SettingsUpdate
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	cfg := s.engine.Configure(in)
	ops.Audit(s.opsDB, "sync.settings", nil, "", "更新同步配置")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": cfg})
}

func (s *Server) handleSyncRun(w http.ResponseWriter, r *http.Request) {
	result := s.engine.SyncAll(nil)
	ops.Audit(s.opsDB, "sync.run", nil, "", "手动触发全量同步")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "total": result.Total, "done": result.Done, "failed": result.Failed})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	items, err := ops.ListAudit(s.opsDB, limit, r.URL.Query().Get("action"))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
