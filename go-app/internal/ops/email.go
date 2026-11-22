// 邮箱面板：配置、验证码提取、邮件解析与拉取（预留接口）。
//
// 用途与 Python 版 app/email.py 一致：只查看与账号关联邮箱收到的通知 /
// 安全邮件 / 登录验证码，不把验证码自动填到任何外部网站。
// 邮箱站通常用「公开取件链接」读邮件，因此这里只保存账号 email 字段
// （邮箱地址或取件链接）与可选的取件接口模板。
package ops

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github-multi-account/internal/config"
	"github-multi-account/internal/github"
)

// EmailConfig 是邮箱面板配置（config/settings.yaml 的 email 段）。
type EmailConfig struct {
	Enabled              bool    `json:"enabled" yaml:"enabled"`
	SiteURL              string  `json:"site_url" yaml:"site_url"`
	APIBase              string  `json:"api_base" yaml:"api_base"`
	Token                string  `json:"token" yaml:"token"`
	FetchIntervalSeconds int     `json:"fetch_interval_seconds" yaml:"fetch_interval_seconds"`
	CodePattern          string  `json:"code_pattern" yaml:"code_pattern"`
	HighlightContext     bool    `json:"highlight_context" yaml:"highlight_context"`
	// 单次刷新最多翻多少页（服务端 hasMore 控制）
	MaxPages int `json:"max_pages" yaml:"max_pages"`
}

// DefaultEmailConfig 是默认配置。
func DefaultEmailConfig() EmailConfig {
	return EmailConfig{
		Enabled:              false,
		SiteURL:              "https://inbox.123456.world",
		APIBase:              "https://inbox.123456.world/i/{email}/json",
		FetchIntervalSeconds: 300,
		HighlightContext:     true,
		MaxPages:             5,
	}
}

// LoadEmailConfig 读取 email 段配置（缺失或损坏时回退默认值）。
func LoadEmailConfig(dir string) EmailConfig {
	cfg := DefaultEmailConfig()
	raw, ok := config.ReadSettings(dir)["email"].(map[string]any)
	if !ok {
		return cfg
	}
	if v, ok := raw["enabled"].(bool); ok {
		cfg.Enabled = v
	}
	if v, ok := raw["site_url"].(string); ok {
		cfg.SiteURL = v
	}
	if v, ok := raw["api_base"].(string); ok {
		cfg.APIBase = v
	}
	if v, ok := raw["token"].(string); ok {
		cfg.Token = v
	}
	if v, ok := asInt(raw["fetch_interval_seconds"]); ok && v > 0 {
		cfg.FetchIntervalSeconds = v
	}
	if v, ok := raw["code_pattern"].(string); ok {
		cfg.CodePattern = v
	}
	if v, ok := raw["highlight_context"].(bool); ok {
		cfg.HighlightContext = v
	}
	if v, ok := asInt(raw["max_pages"]); ok && v > 0 {
		cfg.MaxPages = v
	}
	return cfg
}

// SaveEmailConfig 写回 email 段（读取-合并-写回，保留其他分节）。
func SaveEmailConfig(dir string, cfg EmailConfig) error {
	data := config.ReadSettings(dir)
	data["email"] = cfg
	return config.WriteSettings(dir, data)
}

// EmailMessage 是一封本地缓存的邮件。
type EmailMessage struct {
	ID         int64 `json:"id"`
	AccountID  any   `json:"account_id"`
	Email      string `json:"email"`
	MessageID  string `json:"message_id"`
	Sender     string `json:"sender"`
	Subject    string `json:"subject"`
	Body       string `json:"body"`
	Code       string `json:"code"`
	ReceivedAt string `json:"received_at"`
	CreatedAt  string `json:"created_at"`
}

// AddEmailMessage 保存一封邮件；同账号同 message_id 视为重复。
func AddEmailMessage(
	db *sql.DB, accountID *int, email, messageID, sender, subject, body, receivedAt, code string,
) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if messageID != "" {
		var exists int
		row := db.QueryRow(
			"SELECT 1 FROM email_messages WHERE account_id IS ? AND message_id=? LIMIT 1",
			accountID, messageID)
		if err := row.Scan(&exists); err == nil {
			return false, nil
		}
	}
	_, err := db.Exec(
		"INSERT INTO email_messages"+
			" (account_id, email, message_id, sender, subject, body, code, received_at, created_at)"+
			" VALUES (?,?,?,?,?,?,?,?,?)",
		accountID, email, messageID, sender, subject, body, code, receivedAt, now)
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListEmailMessages 列出本地缓存邮件（可按账号 / 关键字筛选）。
func ListEmailMessages(db *sql.DB, accountID *int, q string, limit int) ([]EmailMessage, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	var cond []string
	var args []any
	if accountID != nil {
		cond = append(cond, "account_id=?")
		args = append(args, *accountID)
	}
	if q != "" {
		cond = append(cond, "(subject LIKE ? OR body LIKE ? OR sender LIKE ? OR email LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like, like, like)
	}
	query := "SELECT id, account_id, email, message_id, sender, subject, body, code, received_at, created_at FROM email_messages"
	if len(cond) > 0 {
		query += " WHERE " + strings.Join(cond, " AND ")
	}
	query += " ORDER BY datetime(received_at) DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EmailMessage{}
	for rows.Next() {
		var m EmailMessage
		var accountID sql.NullInt64
		if err := rows.Scan(&m.ID, &accountID, &m.Email, &m.MessageID, &m.Sender,
			&m.Subject, &m.Body, &m.Code, &m.ReceivedAt, &m.CreatedAt); err != nil {
			return nil, err
		}
		if accountID.Valid {
			m.AccountID = accountID.Int64
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteEmailMessages 删除邮件（按账号或指定 ID）。
func DeleteEmailMessages(db *sql.DB, accountID *int, ids []int) (int, error) {
	var cond []string
	var args []any
	if accountID != nil {
		cond = append(cond, "account_id=?")
		args = append(args, *accountID)
	}
	if len(ids) > 0 {
		placeholders := make([]string, len(ids))
		for i, id := range ids {
			placeholders[i] = "?"
			args = append(args, id)
		}
		cond = append(cond, "id IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(cond) == 0 {
		return 0, nil
	}
	res, err := db.Exec("DELETE FROM email_messages WHERE "+strings.Join(cond, " AND "), args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// AsInt 把 YAML/JSON 里的数值转成 int（供 server 层复用）。
func AsInt(v any) (int, bool) { return asInt(v) }

var (
	emailContextRe = regexp.MustCompile(`(?i)(验证码|校验码|动态码|verification|verify|one[- ]time|code)`)
	emailDigitsRe  = regexp.MustCompile(`\d{4,8}`)
	emailAlnumRe   = regexp.MustCompile(`[A-Za-z0-9]{6,8}`)
)

// ExtractCode 从正文提取验证码（仅用于展示，不参与自动填充）。
func ExtractCode(text, pattern string, context bool) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if pattern != "" {
		if re, err := regexp.Compile(pattern); err == nil {
			m := re.FindStringSubmatch(text)
			if len(m) > 1 {
				return m[1]
			}
			if len(m) == 1 {
				return m[0]
			}
		}
	}
	lines := strings.Split(text, "\n")
	ordered := make([]string, 0, len(lines))
	if context {
		for _, l := range lines {
			if emailContextRe.MatchString(l) {
				ordered = append(ordered, l)
			}
		}
	}
	for _, l := range lines {
		if !context || !emailContextRe.MatchString(l) {
			ordered = append(ordered, l)
		}
	}
	for _, line := range ordered {
		if m := emailDigitsRe.FindString(line); m != "" && digitBoundaryOK(line, m) {
			return m
		}
	}
	for _, line := range ordered {
		for _, m := range emailAlnumRe.FindAllString(line, -1) {
			if alnumBoundaryOK(line, m) && containsDigit(m) {
				return m
			}
		}
	}
	return ""
}

func digitBoundaryOK(line, tok string) bool {
	i := strings.Index(line, tok)
	if i < 0 {
		return true
	}
	if i > 0 && isDigit(line[i-1]) {
		return false
	}
	end := i + len(tok)
	return end >= len(line) || !isDigit(line[end])
}

func alnumBoundaryOK(line, tok string) bool {
	i := strings.Index(line, tok)
	if i < 0 {
		return true
	}
	if i > 0 && isAlnum(line[i-1]) {
		return false
	}
	end := i + len(tok)
	return end >= len(line) || !isAlnum(line[end])
}

func isDigit(b byte) bool      { return b >= '0' && b <= '9' }
func isAlnum(b byte) bool      { return isDigit(b) || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func containsDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if isDigit(s[i]) {
			return true
		}
	}
	return false
}

// BuildFetchURL 把账号 email 字段转换成取件 URL（无法构造时返回空）。
func BuildFetchURL(email string, cfg EmailConfig) string {
	target := strings.TrimSpace(email)
	if target == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(target), "http://") ||
		strings.HasPrefix(strings.ToLower(target), "https://") {
		return target
	}
	template := strings.TrimSpace(cfg.APIBase)
	if template == "" {
		return ""
	}
	return strings.ReplaceAll(template, "{email}", url.QueryEscape(target))
}

// ParseMessages 把接口响应解析成统一邮件结构（与 Python 版规则一致）。
func ParseMessages(payload any, accountEmail string) []map[string]any {
	var items []any
	switch p := payload.(type) {
	case []any:
		items = p
	case map[string]any:
		for _, key := range []string{"messages", "mails", "items", "data"} {
			if list, ok := p[key].([]any); ok {
				items = list
				break
			}
		}
		if len(items) == 0 {
			items = []any{p}
		}
	case string:
		if strings.TrimSpace(p) != "" {
			items = []any{map[string]any{"body": p}}
		}
	}
	out := []map[string]any{}
	for _, raw := range items {
		it, ok := raw.(map[string]any)
		if !ok {
			if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
				it = map[string]any{"body": s}
			} else {
				continue
			}
		}
		body := firstString(it, "body", "text", "content", "html")
		sender := firstString(it, "sender", "from", "from_name", "sender_name")
		emailAddr := firstString(it, "email")
		if emailAddr == "" {
			emailAddr = accountEmail
		}
		out = append(out, map[string]any{
			"email":       emailAddr,
			"sender":      truncate(sender, 200),
			"subject":     truncate(firstString(it, "subject", "title"), 300),
			"body":        truncate(body, 20000),
			"message_id":  truncate(firstString(it, "id", "message_id"), 200),
			"received_at": truncate(firstString(it, "received_at", "date", "created_at"), 40),
		})
	}
	return out
}

// ParsePayload 解析取件接口响应，返回 (邮件列表, 顶层验证码)。
//
// 「邮笺」响应形态：{ok, email, verificationCode, latestMessage, messages[], total,
// page, pageSize, hasMore, expiresAt}；兼容 mails/items/data 等包装。
func ParsePayload(payload any, accountEmail string) ([]map[string]any, string) {
	m, ok := payload.(map[string]any)
	if !ok {
		return ParseMessages(payload, accountEmail), ""
	}
	verify := ""
	if s, ok := m["verificationCode"].(string); ok {
		verify = s
	}
	var raw any = []any{}
	if list, ok := m["messages"].([]any); ok {
		raw = list
	} else {
		for _, key := range []string{"mails", "items", "data"} {
			if list, ok := m[key].([]any); ok {
				raw = list
				break
			}
		}
	}
	parsed := ParseMessages(raw, accountEmail)
	if len(parsed) > 0 && parsed[0]["email"] == "" {
		if s, ok := m["email"].(string); ok && s != "" {
			parsed[0]["email"] = s
		} else {
			parsed[0]["email"] = accountEmail
		}
	}
	return parsed, verify
}

// withPage 给取件 URL 追加分页参数（page 从 1 开始）。
func withPage(u string, page int) string {
	if page <= 1 {
		return u
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "page=" + strconv.Itoa(page)
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			return t
		case map[string]any:
			if s, ok := t["name"].(string); ok && s != "" {
				return s
			}
			if s, ok := t["address"].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// FetchAccountMail 拉取单个账号邮箱的邮件（通用预留实现）。
func FetchAccountMail(client *http.Client, accountEmail string, cfg EmailConfig) map[string]any {
	url := BuildFetchURL(accountEmail, cfg)
	if url == "" {
		return map[string]any{
			"ok":     false,
			"reason": "not_configured",
			"hint":   "账号 email 需填写取件链接，或在 email.api_base 配置取件接口模板（支持 {email}）",
		}
	}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return map[string]any{"ok": false, "reason": "bad_url", "url": url}
	}
	req.Header.Set("User-Agent", github.UserAgent)
	req.Header.Set("Accept", "application/json,text/plain,*/*")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	maxPages := cfg.MaxPages
	if maxPages < 1 {
		maxPages = 5
	}
	all := []map[string]any{}
	verifyCode := ""
	for page := 1; page <= maxPages; page++ {
		pageURL := withPage(url, page)
		req, err := http.NewRequest(http.MethodGet, pageURL, nil)
		if err != nil {
			return map[string]any{"ok": false, "reason": "bad_url", "url": pageURL}
		}
		req.Header.Set("User-Agent", github.UserAgent)
		req.Header.Set("Accept", "application/json,text/plain,*/*")
		if cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return map[string]any{"ok": false, "reason": "network_error", "error": err.Error(), "url": pageURL}
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			if page == 1 {
				return map[string]any{"ok": false, "reason": "http_error", "status": resp.StatusCode, "url": pageURL}
			}
			break
		}
		var payload any
		if strings.Contains(resp.Header.Get("Content-Type"), "json") {
			if json.Unmarshal(body, &payload) != nil {
				payload = string(body)
			}
		} else {
			payload = string(body)
		}
		batch, code := ParsePayload(payload, accountEmail)
		if verifyCode == "" {
			verifyCode = code
		}
		all = append(all, batch...)
		hasMore, _ := payload.(map[string]any)["hasMore"].(bool)
		if !hasMore || len(batch) == 0 {
			break
		}
	}
	return map[string]any{
		"ok": true, "url": url, "fetched": len(all),
		"messages": all, "verificationCode": verifyCode, "account_email": accountEmail,
	}
}
