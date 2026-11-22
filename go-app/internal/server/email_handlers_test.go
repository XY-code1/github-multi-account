package server

import (
	"net/http"
	"strings"
	"testing"

	"github-multi-account/internal/config"
)

func TestEmailSettingsImportAndListFlow(t *testing.T) {
	h, cfg, _ := newTestEnv(t)
	if _, err := cfg.Add(config.Account{Label: "邮箱号", Username: "u1", Token: "t", Email: "me@inbox.example"}); err != nil {
		t.Fatalf("建账号失败: %v", err)
	}

	w := do(h, http.MethodPost, "/api/email/settings",
		`{"enabled":true,"site_url":"https://inbox.123456.world"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("保存邮箱设置失败: %d %s", w.Code, w.Body.String())
	}
	got := do(h, http.MethodGet, "/api/email/settings", "", nil)
	if !strings.Contains(got.Body.String(), `"enabled":true`) ||
		!strings.Contains(got.Body.String(), `"site_url":"https://inbox.123456.world"`) {
		t.Fatalf("邮箱设置回读不正确: %s", got.Body.String())
	}

	w = do(h, http.MethodPost, "/api/email/import", `{"messages":[
		{"email":"me@inbox.example","sender":"GitHub","subject":"登录验证码",
		 "body":"验证码 246810，5 分钟内有效","receivedAt":"2026-11-22T01:00:00Z","messageId":"m1"},
		{"email":"me@inbox.example","messageId":"m1","body":"重复"}
	]}`, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"added":1`) {
		t.Fatalf("导入失败: %d %s", w.Code, w.Body.String())
	}

	list := do(h, http.MethodGet, "/api/email/messages", "", nil)
	if !strings.Contains(list.Body.String(), `"code":"246810"`) {
		t.Fatalf("验证码未提取: %s", list.Body.String())
	}
	empty := do(h, http.MethodGet, "/api/email/messages?q=zzz", "", nil)
	if !strings.Contains(empty.Body.String(), `"total":0`) {
		t.Fatalf("关键字筛选失败: %s", empty.Body.String())
	}

	del := do(h, http.MethodDelete, "/api/email/messages", `{"ids":[1]}`, nil)
	if !strings.Contains(del.Body.String(), `"deleted":1`) {
		t.Fatalf("删除失败: %s", del.Body.String())
	}
}

func TestEmailRefreshDisabled(t *testing.T) {
	h := newTestHandler(t)
	w := do(h, http.MethodPost, "/api/email/refresh", `{}`, nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Fatalf("未启用时应返回 ok=false: %d %s", w.Code, w.Body.String())
	}
}
