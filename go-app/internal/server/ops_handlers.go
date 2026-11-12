// 运维中心 HTTP 处理器：全局聚合、分页账号列表、同步引擎控制、审计日志。
package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"

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
			ok, status := s.engine.SyncAccount(a)
			ops.Audit(s.opsDB, "accounts.sync", &a.ID, a.Username, "手动同步")
			writeJSON(w, http.StatusOK, map[string]any{
				"id":     a.ID,
				"ok":     ok,
				"status": status,
			})
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
