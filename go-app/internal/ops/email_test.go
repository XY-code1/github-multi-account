package ops

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github-multi-account/internal/config"
	"github-multi-account/internal/store"
)

func newEmailTestDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := Init(st.DB()); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	return st.DB()
}

func TestExtractCode(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{"上下文优先", "您好。\n验证码：823456，10 分钟内有效。", "823456"},
		{"纯数字", "您的代码是 483920。", "483920"},
		{"混合型", "Verification code: A7B9C2D4", "A7B9C2D4"},
		{"长数字不算", "订单号 123456789 已发货", ""},
		{"空文本", "", ""},
	}
	for _, c := range cases {
		if got := ExtractCode(c.text, "", true); got != c.want {
			t.Fatalf("%s: 期望 %q，得到 %q", c.name, c.want, got)
		}
	}
}

func TestExtractCodeCustomPattern(t *testing.T) {
	if got := ExtractCode("CODE[abcd-1234] 有效", `CODE\[([A-Za-z0-9-]+)\]`, true); got != "abcd-1234" {
		t.Fatalf("自定义正则未生效: %q", got)
	}
}

func TestBuildFetchURL(t *testing.T) {
	cfg := DefaultEmailConfig()
	if got := BuildFetchURL("https://inbox.example/x?token=abc", cfg); got != "https://inbox.example/x?token=abc" {
		t.Fatalf("完整链接应原样返回: %q", got)
	}
	if got := BuildFetchURL("kRDI-abc_123", cfg); got != "https://inbox.123456.world/i/kRDI-abc_123/json" {
		t.Fatalf("默认模板拼接不正确: %q", got)
	}
	cfg.APIBase = "https://inbox.example/api?address={email}"
	if got := BuildFetchURL("a@b.com", cfg); got != "https://inbox.example/api?address=a%40b.com" {
		t.Fatalf("模板拼接不正确: %q", got)
	}
	cfg.APIBase = ""
	if got := BuildFetchURL("a@b.com", cfg); got != "" {
		t.Fatalf("未配置模板时应返回空串: %q", got)
	}
}

func TestParsePayload(t *testing.T) {
	payload := map[string]any{
		"ok": true, "email": "x@icloud.com", "verificationCode": "123456",
		"messages": []any{
			map[string]any{"id": "m1", "from": "GitHub", "subject": "登录", "body": "code 111111"},
		},
		"hasMore": false, "page": 1,
	}
	msgs, code := ParsePayload(payload, "x@icloud.com")
	if code != "123456" {
		t.Fatalf("顶层验证码未提取: %q", code)
	}
	if len(msgs) != 1 || msgs[0]["email"] != "x@icloud.com" || msgs[0]["message_id"] != "m1" {
		t.Fatalf("解析结果不正确: %+v", msgs)
	}
}

func TestFetchAccountMailPaginates(t *testing.T) {
	pages := map[string]map[string]any{
		"1": {"ok": true, "email": "x@icloud.com", "verificationCode": "123456",
			"messages": []any{
				map[string]any{"id": "a", "from": "GitHub", "subject": "登录", "body": "code 111111",
					"date": "2026-11-22T01:00:00Z"},
			}, "hasMore": true, "page": 1},
		"2": {"ok": true, "messages": []any{
			map[string]any{"id": "b", "from": "Apple", "subject": "安全", "body": "验证码 222222",
				"date": "2026-11-22T02:00:00Z"},
		}, "hasMore": false, "page": 2},
	}
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		if page == "" {
			page = "1"
		}
		calls = append(calls, page)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(pages[page])
	}))
	defer srv.Close()

	cfg := DefaultEmailConfig()
	cfg.APIBase = srv.URL + "/i/{email}/json"
	res := FetchAccountMail(srv.Client(), "tok", cfg)
	if res["ok"] != true {
		t.Fatalf("拉取失败: %+v", res)
	}
	if res["fetched"] != 2 {
		t.Fatalf("应拉取 2 封: %+v", res["fetched"])
	}
	if res["verificationCode"] != "123456" {
		t.Fatalf("验证码未回传: %+v", res["verificationCode"])
	}
	if len(calls) != 2 || calls[0] != "1" || calls[1] != "2" {
		t.Fatalf("分页请求不正确: %v", calls)
	}
}

func TestParseMessages(t *testing.T) {
	list := ParseMessages([]any{
		map[string]any{"id": "1", "from": map[string]any{"name": "GitHub", "address": "x@y"},
			"subject": "登录", "body": "验证码 135790", "received_at": "2026-11-22T01:00:00Z"},
	}, "me@inbox")
	if len(list) != 1 {
		t.Fatalf("应解析出 1 封: %d", len(list))
	}
	if list[0]["sender"] != "GitHub" || list[0]["email"] != "me@inbox" {
		t.Fatalf("解析字段不正确: %+v", list[0])
	}
	wrapped := ParseMessages(map[string]any{"messages": []any{map[string]any{"subject": "a"}}}, "e")
	if len(wrapped) != 1 {
		t.Fatalf("包装形态解析失败: %d", len(wrapped))
	}
	raw := ParseMessages("纯文本 998877", "e")
	if len(raw) != 1 || raw[0]["body"] != "纯文本 998877" {
		t.Fatalf("纯文本解析失败: %+v", raw)
	}
}

func TestEmailMessageCRUD(t *testing.T) {
	db := newEmailTestDB(t)
	id1, id2 := 1, 2

	if ok, err := AddEmailMessage(db, &id1, "a@b", "z1", "s", "t", "b", "", ""); !ok || err != nil {
		t.Fatalf("首次写入应成功: ok=%v err=%v", ok, err)
	}
	if ok, _ := AddEmailMessage(db, &id1, "a@b", "z1", "s", "t", "b", "", ""); ok {
		t.Fatal("重复 message_id 应被跳过")
	}
	if _, err := AddEmailMessage(db, &id2, "c@d", "z2", "s", "t2", "b2", "", ""); err != nil {
		t.Fatalf("写入第二个账号失败: %v", err)
	}

	items, err := ListEmailMessages(db, &id1, "", 100)
	if err != nil || len(items) != 1 {
		t.Fatalf("按账号筛选失败: %v %+v", err, items)
	}
	if items[0].AccountID != int64(1) {
		t.Fatalf("account_id 未正确返回: %+v", items[0].AccountID)
	}
	all, _ := ListEmailMessages(db, nil, "", 100)
	if len(all) != 2 {
		t.Fatalf("应有 2 封: %d", len(all))
	}
	matched, _ := ListEmailMessages(db, nil, "t2", 100)
	if len(matched) != 1 {
		t.Fatalf("关键字筛选失败: %d", len(matched))
	}
	if n, _ := DeleteEmailMessages(db, &id1, nil); n != 1 {
		t.Fatalf("删除条数应为 1: %d", n)
	}
	rest, _ := ListEmailMessages(db, nil, "", 100)
	if len(rest) != 1 {
		t.Fatalf("删除后应剩 1 封: %d", len(rest))
	}
}

func TestEmailConfigRoundtrip(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultEmailConfig()
	cfg.Enabled = true
	cfg.SiteURL = "https://inbox.123456.world"
	cfg.APIBase = "https://inbox.123456.world/api?address={email}"
	if err := SaveEmailConfig(dir, cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got := LoadEmailConfig(dir)
	if !got.Enabled || got.APIBase != cfg.APIBase || got.SiteURL != cfg.SiteURL {
		t.Fatalf("配置回读不一致: %+v", got)
	}
}

func TestParseInboxList(t *testing.T) {
	raw := "alice https://inbox.123456.world/i/tok-a/json\n" +
		"bob@x.com\nhttps://inbox.123456.world/i/tok-b/json\n" +
		"alice https://inbox.123456.world/i/tok-a/json\n" + // 重复，应去重
		"https://inbox.123456.world/i/tok-c/json" // 仅链接
	items := ParseInboxList(raw)
	if len(items) != 3 {
		t.Fatalf("应解析出 3 条（去重后），得到 %d: %+v", len(items), items)
	}
	if items[0].Email != "alice" || items[0].URL == "" {
		t.Fatalf("同行解析不正确: %+v", items[0])
	}
	if items[1].Email != "bob@x.com" {
		t.Fatalf("相邻两行解析不正确: %+v", items[1])
	}
	if items[2].Email != "" {
		t.Fatalf("仅链接应无标识: %+v", items[2])
	}
}

func TestParseInboxListJSON(t *testing.T) {
	items := ParseInboxList(`[{"email":"a@b.com","url":"https://x/1"},{"name":"carol","token":"tok"}]`)
	if len(items) != 2 {
		t.Fatalf("JSON 解析数量不正确: %d", len(items))
	}
	if items[1].Email != "carol" || items[1].URL != "tok" {
		t.Fatalf("name/token 别名解析不正确: %+v", items[1])
	}
}

func TestMatchInboxAccount(t *testing.T) {
	accounts := []config.Account{
		{ID: 1, Username: "alice", Email: "a@b.com"},
		{ID: 2, Username: "bob"},
	}
	if id, name := MatchInboxAccount("a@b.com", accounts); id != 1 || name != "alice" {
		t.Fatalf("按邮箱匹配失败: %d %s", id, name)
	}
	if id, _ := MatchInboxAccount("bob", accounts); id != 2 {
		t.Fatalf("按标识匹配用户名失败: %d", id)
	}
	if id, _ := MatchInboxAccount("bob@x.com", accounts); id != 2 {
		t.Fatalf("按邮箱名匹配用户名失败: %d", id)
	}
	if id, _ := MatchInboxAccount("nobody@x.com", accounts); id != 0 {
		t.Fatalf("未匹配应返回 0: %d", id)
	}
}
