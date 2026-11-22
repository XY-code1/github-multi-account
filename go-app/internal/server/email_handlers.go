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
	var accountID, inboxID *int
	if raw := q.Get("accountId"); raw != "" {
		if id, err := strconv.Atoi(raw); err == nil {
			accountID = &id
		}
	}
	if raw := q.Get("inboxId"); raw != "" {
		if id, err := strconv.Atoi(raw); err == nil {
			inboxID = &id
		}
	}
	cfg := ops.LoadEmailConfig(emailConfigDir(s))
	items, err := ops.ListEmailMessages(
		s.opsDB, accountID, q.Get("q"), intParam(q.Get("limit"), 100), inboxID,
		q.Get("sort"), q.Get("hasCode") == "1" || q.Get("hasCode") == "true",
	)
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
			body, strOf(m, "receivedAt"), code, nil)
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

	saveMessages := func(res map[string]any, emailFallback string, accID, inboxID *int) int {
		added := 0
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
					s.opsDB, accID, firstNonEmpty(email, emailFallback),
					strOf(m, "message_id"), strOf(m, "sender"), strOf(m, "subject"),
					body, strOf(m, "received_at"), code, inboxID); err == nil {
					added++
				}
			}
		}
		return added
	}

	for _, a := range accounts {
		if accountID != nil && a.ID != *accountID {
			continue
		}
		if a.Email == "" {
			continue
		}
		res := ops.FetchInboxMail(client, a.Email, cfg, nil)
		acc := a.ID
		added := saveMessages(res, a.Email, &acc, nil)
		entry := map[string]any{"id": a.ID, "username": a.Username, "kind": "account", "added": added}
		if v, ok := res["verificationCode"].(string); ok && v != "" {
			entry["verificationCode"] = v
		}
		for _, k := range []string{"ok", "reason", "hint", "url"} {
			if v, exists := res[k]; exists {
				entry[k] = v
			}
		}
		count += added
		results = append(results, entry)
	}

	// 独立邮箱（未匹配账号的导入条目）
	if accountID == nil {
		if inboxes, err := ops.ListInboxes(s.opsDB); err == nil {
			for _, ib := range inboxes {
				ibID := int(ib.ID)
				res := ops.FetchInboxMail(client, ib.URL, cfg, &ibID)
				fallback := firstNonEmpty(ib.Email, ib.URL)
				added := saveMessages(res, fallback, nil, &ibID)
				entry := map[string]any{"id": ib.ID, "email": fallback, "kind": "inbox", "added": added}
				if v, ok := res["verificationCode"].(string); ok && v != "" {
					entry["verificationCode"] = v
				}
				for _, k := range []string{"ok", "reason", "hint", "url"} {
					if v, exists := res[k]; exists {
						entry[k] = v
					}
				}
				count += added
				results = append(results, entry)
			}
		}
	}
	if len(results) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "reason": "no_account", "hint": "没有填写邮箱的账号或独立邮箱",
		})
		return
	}
	ops.Audit(s.opsDB, "email.refresh", nil, "", "刷新邮箱："+strconv.Itoa(len(results))+" 个")
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

// handleEmailInboxesParse 解析邮笺导出的邮箱清单，并匹配现有账号。
func (s *Server) handleEmailInboxesParse(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	accounts, err := s.cfg.Accounts()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	items := []map[string]any{}
	for _, c := range ops.ParseInboxList(in.Text) {
		id, username := ops.MatchInboxAccount(c.Email, accounts)
		items = append(items, map[string]any{
			"email": c.Email, "url": c.URL,
			"accountId": id, "accountUsername": username,
		})
	}
	list := []map[string]any{}
	for _, a := range accounts {
		list = append(list, map[string]any{
			"id": a.ID, "username": a.Username, "label": a.Label, "email": a.Email,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": items, "accounts": list})
}

// handleEmailInboxesImport 把邮箱清单按账号回填（只更新关联邮箱字段）。
func (s *Server) handleEmailInboxesImport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	updated := 0
	for _, item := range in.Items {
		url := strOf(item, "url")
		if url == "" {
			continue
		}
		if rawID, ok := item["accountId"].(float64); ok && int(rawID) != 0 {
			if err := s.cfg.SetEmail(int(rawID), url); err == nil {
				updated++
			}
			continue
		}
		// 未匹配账号：存为独立邮箱
		if _, err := ops.AddInbox(s.opsDB, strOf(item, "email"), url); err == nil {
			updated++
		}
	}
	ops.Audit(s.opsDB, "email.inboxes.import", nil, "",
		"导入邮箱清单：关联/新增 "+strconv.Itoa(updated)+" 条")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "updated": updated})
}

// handleEmailInboxes 返回独立邮箱清单。
func (s *Server) handleEmailInboxes(w http.ResponseWriter, r *http.Request) {
	items, err := ops.ListInboxes(s.opsDB)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleEmailInboxAdd 新增独立邮箱。
func (s *Server) handleEmailInboxAdd(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
		URL   string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "请求格式不正确"})
		return
	}
	item, err := ops.AddInbox(s.opsDB, in.Email, in.URL)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if item == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "url 不能为空或已存在"})
		return
	}
	// 审计只记录邮箱名，不写取件链接（含 token）
	ops.Audit(s.opsDB, "email.inboxes.add", nil, "", "新增独立邮箱："+firstNonEmpty(item.Email, "未命名"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": item})
}

// handleEmailInboxDelete 删除独立邮箱。
func (s *Server) handleEmailInboxDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "独立邮箱不存在"})
		return
	}
	deleted, err := ops.DeleteInbox(s.opsDB, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"detail": err.Error()})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]any{"detail": "独立邮箱不存在"})
		return
	}
	ops.Audit(s.opsDB, "email.inboxes.delete", nil, "", "删除独立邮箱 #"+strconv.Itoa(id))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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
