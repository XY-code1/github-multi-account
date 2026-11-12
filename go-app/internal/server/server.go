// Package server 提供 HTTP API 与静态资源服务。
package server

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github-multi-account/internal/config"
	"github-multi-account/internal/notify"
	"github-multi-account/internal/ops"
	"github-multi-account/internal/service"
	"github-multi-account/internal/store"
)

// Version 是当前程序版本（构建时可覆盖）。
var Version = "1.1.0"

// Server 是 HTTP 服务。
type Server struct {
	cfg     *config.Store
	svc     *service.Service
	store   *store.Store
	assets  fs.FS
	mux     *http.ServeMux
	baseDir string
	poller  *notify.Poller
	opsDB   *sql.DB
	engine  *ops.Engine
	cfgDir  string
}

// New 创建服务并注册路由。
func New(cfg *config.Store, svc *service.Service, st *store.Store, assets fs.FS) *Server {
	s := &Server{cfg: cfg, svc: svc, store: st, assets: assets, mux: http.NewServeMux()}
	s.routes()
	return s
}

// SetBaseDir 设置配置根目录（通知渠道用）。
func (s *Server) SetBaseDir(dir string) { s.baseDir = dir }

// SetPoller 设置通知轮询器。
func (s *Server) SetPoller(p *notify.Poller) { s.poller = p }

// Handler 返回带 CSRF 防护的处理器。
func (s *Server) Handler() http.Handler { return s.csrf(s.mux) }

func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeJSON(w, http.StatusForbidden, map[string]any{"detail": "跨站请求被拒绝"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/accounts", s.handleAccounts)
	s.mux.HandleFunc("POST /api/accounts", s.handleCreateAccount)
	s.mux.HandleFunc("DELETE /api/accounts/{id}", s.handleDeleteAccount)
	s.mux.HandleFunc("POST /api/verify", s.handleVerify)
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/search", s.handleSearch)
	s.mux.HandleFunc("GET /api/status/{id}", s.handleAccountStatus)
	s.mux.HandleFunc("POST /api/accounts/{id}/verify", s.handleVerifyAccount)
	s.mux.HandleFunc("GET /api/accounts/export", s.handleExport)
	s.mux.HandleFunc("POST /api/accounts/export", s.handleExportPost)
	s.mux.HandleFunc("POST /api/accounts/import", s.handleImport)
	s.mux.HandleFunc("GET /api/history/{id}", s.handleHistory)
	s.mux.HandleFunc("POST /api/snapshot/{id}", s.handleSnapshot)
	s.mux.HandleFunc("GET /api/version", s.handleVersion)
	s.mux.HandleFunc("GET /api/update-check", s.handleUpdateCheck)
	s.mux.HandleFunc("GET /api/notifications/settings", s.handleGetNotificationSettings)
	s.mux.HandleFunc("POST /api/notifications/settings", s.handleSaveNotificationSettings)
	s.mux.HandleFunc("POST /api/notifications/test", s.handleTestNotification)
	// 运维中心
	s.mux.HandleFunc("GET /api/dashboard/summary", s.handleDashboardSummary)
	s.mux.HandleFunc("GET /api/dashboard/tasks", s.handleDashboardTasks)
	s.mux.HandleFunc("GET /api/dashboard/activities", s.handleDashboardActivities)
	s.mux.HandleFunc("POST /api/accounts/batch-refresh", s.handleBatchRefresh)
	s.mux.HandleFunc("POST /api/accounts/{id}/sync", s.handleSyncAccount)
	s.mux.HandleFunc("GET /api/tasks", s.handleTasks)
	s.mux.HandleFunc("POST /api/sync/settings", s.handleSyncSettings)
	s.mux.HandleFunc("POST /api/sync/run", s.handleSyncRun)
	s.mux.HandleFunc("GET /api/audit", s.handleAudit)
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /static/", s.handleStatic)
}

func pathID(r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		return 0, false
	}
	return id, true
}

type accountIn struct {
	Label    string   `json:"label"`
	Username string   `json:"username"`
	Token    string   `json:"token"`
	Group    string   `json:"group"`
	Type     string   `json:"type"`
	Tags     []string `json:"tags"`
	Note     string   `json:"note"`
	Alias    string   `json:"alias"`
}

func (s *Server) handleCreateAccount(w http.ResponseWriter, r *http.Request) {
	var in accountIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	if in.Username == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "username 不能为空"})
		return
	}
	if in.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "token 不能为空"})
		return
	}
	acc, err := s.cfg.Add(config.Account{
		Label:    in.Label,
		Username: in.Username,
		Token:    in.Token,
		Group:    in.Group,
		Type:     in.Type,
		Tags:     in.Tags,
		Note:     in.Note,
		Alias:    in.Alias,
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	ops.Audit(s.opsDB, "accounts.create", &acc.ID, acc.Username, "新增/更新账号")
	writeJSON(w, http.StatusOK, map[string]any{
		"id":           acc.ID,
		"label":        acc.Label,
		"username":     acc.Username,
		"group":        acc.Group,
		"type":         acc.Type,
		"token_masked": acc.MaskedToken(),
	})
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
		return
	}
	username, found, err := s.cfg.Delete(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
		return
	}
	if username != "" && s.store != nil {
		_, _ = s.store.DeleteHistory(username)
	}
	ops.Audit(s.opsDB, "accounts.delete", &id, username, "删除账号")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	var in accountIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	status, user, msg := service.Verify(in.Username, in.Token)
	out := map[string]any{"status": status, "user": user, "error": nil}
	if msg != "" {
		out["error"] = msg
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if len(accounts) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"accounts": []any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": s.svc.Summaries(accounts)})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		kind = "all"
	}
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, service.GlobalSearch(accounts, q, kind))
}

func (s *Server) handleAccountStatus(w http.ResponseWriter, r *http.Request) {
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
			writeJSON(w, http.StatusOK, map[string]any{"account": s.svc.Detail(a, false)})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
}

func (s *Server) handleVerifyAccount(w http.ResponseWriter, r *http.Request) {
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
			st := s.svc.Summary(a)
			writeJSON(w, http.StatusOK, map[string]any{
				"id":     st.ID,
				"status": st.Status,
				"user":   st.User,
				"error":  st.Error,
			})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
}

// handleExport 导出账号信息备份（不含凭据 / Token）。
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	data, err := s.cfg.Export(false, "")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	ops.Audit(s.opsDB, "accounts.export", nil, "", "导出账号信息（不含凭据）")
	w.Header().Set("Content-Disposition", "attachment; filename=github-accounts-backup.json")
	writeJSON(w, http.StatusOK, data)
}

type exportIn struct {
	Password string `json:"password"`
}

// handleExportPost 导出凭据备份（含 Token，password 非空时加密）。
func (s *Server) handleExportPost(w http.ResponseWriter, r *http.Request) {
	var in exportIn
	_ = json.NewDecoder(r.Body).Decode(&in)
	data, err := s.cfg.Export(true, in.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	ops.Audit(s.opsDB, "accounts.export_credentials", nil, "", "导出凭据备份")
	filename := "github-accounts-backup-credentials.json"
	if in.Password != "" {
		filename = "github-accounts-backup-encrypted.json"
	}
	w.Header().Set("Content-Disposition", "attachment; filename="+filename)
	writeJSON(w, http.StatusOK, data)
}

type importIn struct {
	Password   string `json:"password"`
	Accounts   []any  `json:"accounts"`
	Encrypted  bool   `json:"encrypted"`
	Salt       string `json:"salt"`
	Iterations int    `json:"iterations"`
	Payload    string `json:"payload"`
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	var in importIn
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	payload := map[string]any{
		"encrypted":  in.Encrypted,
		"salt":       in.Salt,
		"iterations": in.Iterations,
		"payload":    in.Payload,
	}
	if in.Accounts != nil {
		payload["accounts"] = in.Accounts
	}
	added, updated, skipped, err := s.cfg.Import(payload, in.Password)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "updated": updated, "skipped": skipped})
}

func (s *Server) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"channels": notify.LoadChannels(s.baseDir),
		"types":    notify.ChannelTypes,
	})
}

func (s *Server) handleSaveNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Channels []notify.Channel `json:"channels"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	valid := make([]notify.Channel, 0, len(in.Channels))
	for _, c := range in.Channels {
		for _, t := range notify.ChannelTypes {
			if c.Type == t {
				valid = append(valid, c)
				break
			}
		}
	}
	if err := notify.SaveChannels(s.baseDir, valid); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if len(valid) > 0 && s.poller != nil && !s.poller.IsRunning() {
		s.poller.Start()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channels": valid})
}

func (s *Server) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	channels := notify.LoadChannels(s.baseDir)
	if len(channels) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请先配置通知渠道"})
		return
	}
	n := notify.SendAll(channels, "GitHub 仪表盘", "这是一条测试通知，收到说明配置成功 ✓", "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sent": n})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version": Version,
		"repo":    "https://github.com/XY-code1/github-multi-account",
	})
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/XY-code1/github-multi-account/releases/latest")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "无法连接 GitHub"})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     false,
			"error":  fmt.Sprintf("GitHub 返回 %d（仓库可能是私有的，无法检查更新）", resp.StatusCode),
			"latest": "",
		})
		return
	}
	var rel struct {
		TagName string `json:"tag_name"`
		HTMLURL string `json:"html_url"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&rel)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"current": Version,
		"latest":  rel.TagName,
		"url":     rel.HTMLURL,
	})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	for _, a := range accounts {
		if a.ID == id {
			h, err := s.store.History(a.Username, limit)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"history": h, "stats": s.store.Stats(a.Username)})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
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
			st := s.svc.Detail(a, true)
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":     true,
				"stats":  s.store.Stats(a.Username),
				"status": st.Status,
			})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"detail": "账号不存在"})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(s.assets, "index.html")
	if err != nil {
		http.Error(w, "index.html not found", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	sub, err := fs.Sub(s.assets, "static")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.StripPrefix("/static/", http.FileServerFS(sub)).ServeHTTP(w, r)
}
