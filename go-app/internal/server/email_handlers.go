// 邮箱面板 HTTP 处理器：配置、邮件列表、导入、刷新（拉取预留）。
package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github-multi-account/internal/ops"
)

func emailConfigDir(s *Server) string { return filepath.Join(s.baseDir, "config") }

func (s *Server) handleGetEmailSettings(w http.ResponseWriter, r *http.Request) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	type acct struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
		Label    string `json:"label"`
		Email    string `json:"email"`
	}
	list := make([]acct, 0, len(accounts))
	for _, a := range accounts {
		list = append(list, acct{ID: a.ID, Username: a.Username, Label: a.Label, Email: a.Email})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": ops.LoadEmailConfig(emailConfigDir(s)),
		"accounts": list,
	})
}

func (s *Server) handleSaveEmailSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	cfg := ops.LoadEmailConfig(emailConfigDir(s))
	if v, ok := in["enabled"].(bool); ok {
		cfg.Enabled = v
	}
	if v, ok := in["site_url"].(string); ok {
		cfg.SiteURL = v
	}
	if v, ok := in["api_base"].(string); ok {
		cfg.APIBase = v
	}
	if v, ok := in["token"].(string); ok {
		cfg.Token = v
	}
	if v, ok := ops.AsInt(in["fetch_interval_seconds"]); ok && v > 0 {
		cfg.FetchIntervalSeconds = v
	}
	if v, ok := in["code_pattern"].(string); ok {
		cfg.CodePattern = v
	}
	if v, ok := in["highlight_context"].(bool); ok {
		cfg.HighlightContext = v
	}
	if err := ops.SaveEmailConfig(emailConfigDir(s), cfg); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	ops.Audit(s.opsDB, "email.settings", nil, "", "更新邮箱面板配置")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "settings": cfg})
}

func (s *Server) handleEmailMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var accountID *int
	if raw := q.Get("accountId"); raw != "" {
		if id, err := strconv.Atoi(raw); err == nil {
			accountID = &id
		}
	}
	cfg := ops.LoadEmailConfig(emailConfigDir(s))
	items, err := ops.ListEmailMessages(s.opsDB, accountID, q.Get("q"), intParam(q.Get("limit"), 100))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	for i := range items {
		if items[i].Code == "" {
			items[i].Code = ops.ExtractCode(items[i].Body, cfg.CodePattern, cfg.HighlightContext)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (s *Server) handleEmailImport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountID *int            `json:"accountId"`
		Messages  []map[string]any `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	accounts, _ := s.cfg.Accounts()
	byID := map[int]string{}
	byEmail := map[string]string{}
	for _, a := range accounts {
		byID[a.ID] = a.Email
		if a.Email != "" {
			byEmail[strings.ToLower(a.Email)] = a.Email
		}
	}
	cfg := ops.LoadEmailConfig(emailConfigDir(s))
	added, skipped := 0, 0
	for _, m := range in.Messages {
		var accID *int
		email := strOf(m, "email")
		if in.AccountID != nil {
			id := *in.AccountID
			accID = &id
		} else if email != "" {
			if matched, ok := byEmail[strings.ToLower(email)]; ok {
				_ = matched
				for _, a := range accounts {
					if strings.EqualFold(a.Email, email) {
						id := a.ID
						accID = &id
						break
					}
				}
			}
		}
		body := strOf(m, "body")
		code := ops.ExtractCode(body, cfg.CodePattern, cfg.HighlightContext)
		ok, err := ops.AddEmailMessage(
			s.opsDB, accID, firstNonEmpty(email, byIDValue(byID, accID)),
			strOf(m, "messageId", "id"), strOf(m, "sender"), strOf(m, "subject"),
			body, strOf(m, "receivedAt"), code)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
			return
		}
		if ok {
			added++
		} else {
			skipped++
		}
	}
	ops.Audit(s.opsDB, "email.import", nil, "", "导入邮件 "+strconv.Itoa(added)+" 封（跳过重复 "+strconv.Itoa(skipped)+"）")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "added": added, "skipped": skipped})
}

func (s *Server) handleEmailRefresh(w http.ResponseWriter, r *http.Request) {
	cfg := ops.LoadEmailConfig(emailConfigDir(s))
	if !cfg.Enabled {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "reason": "disabled", "hint": "请先在「邮箱设置」开启 email.enabled",
		})
		return
	}
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	var accountID *int
	if raw := r.URL.Query().Get("accountId"); raw != "" {
		if id, err := strconv.Atoi(raw); err == nil {
			accountID = &id
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	results := []map[string]any{}
	count := 0
	for _, a := range accounts {
		if accountID != nil && a.ID != *accountID {
			continue
		}
		if a.Email == "" {
			continue
		}
		res := ops.FetchAccountMail(client, a.Email, cfg)
		entry := map[string]any{"id": a.ID, "username": a.Username}
		addedAccount := 0
		verifyCode := ""
		if v, ok := res["verificationCode"].(string); ok {
			verifyCode = v
		}
		if msgs, ok := res["messages"].([]map[string]any); ok {
			for idx, m := range msgs {
				email, _ := m["email"].(string)
				body, _ := m["body"].(string)
				code := ops.ExtractCode(body, cfg.CodePattern, cfg.HighlightContext)
				if code == "" && idx == 0 && verifyCode != "" {
					code = verifyCode
				}
				if _, err := ops.AddEmailMessage(
					s.opsDB, &a.ID, firstNonEmpty(email, a.Email),
					strOf(m, "message_id"), strOf(m, "sender"), strOf(m, "subject"),
					body, strOf(m, "received_at"), code); err == nil {
					addedAccount++
				}
			}
			entry["fetched"] = len(msgs)
		}
		entry["added"] = addedAccount
		if verifyCode != "" {
			entry["verificationCode"] = verifyCode
		}
		for _, k := range []string{"ok", "reason", "hint", "url"} {
			if v, exists := res[k]; exists {
				entry[k] = v
			}
		}
		count += addedAccount
		results = append(results, entry)
	}
	ops.Audit(s.opsDB, "email.refresh", nil, "", "刷新邮箱："+strconv.Itoa(len(results))+" 个账号")
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "added": count, "results": results,
	})
}

func (s *Server) handleEmailDelete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AccountID *int  `json:"accountId"`
		IDs       []int `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	n, err := ops.DeleteEmailMessages(s.opsDB, in.AccountID, in.IDs)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	ops.Audit(s.opsDB, "email.delete", nil, "", "删除邮件 "+strconv.Itoa(n)+" 封")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": n})
}

func intParam(raw string, def int) int {
	if raw == "" {
		return def
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return n
	}
	return def
}

func strOf(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func byIDValue(m map[int]string, id *int) string {
	if id == nil {
		return ""
	}
	return m[*id]
}

var _ = strings.TrimSpace
