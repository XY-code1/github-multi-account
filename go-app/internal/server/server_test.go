package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github-multi-account/internal/config"
	"github-multi-account/internal/ops"
	"github-multi-account/internal/service"
	"github-multi-account/internal/store"
)

func buildDocx(t *testing.T, lines []string) []byte {
	t.Helper()
	const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`
	const xmlFooter = `</w:body></w:document>`
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(`<w:p><w:r><w:t>` + line + `</w:t></w:r></w:p>`)
	}
	raw := xmlHeader + sb.String() + xmlFooter

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("创建 docx 条目失败: %v", err)
	}
	if _, err := w.Write([]byte(raw)); err != nil {
		t.Fatalf("写入 docx 失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 docx 失败: %v", err)
	}
	return buf.Bytes()
}

func multipartBody(t *testing.T, file []byte, fields map[string]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "accounts.docx")
	if err != nil {
		t.Fatalf("创建表单文件失败: %v", err)
	}
	if _, err := fw.Write(file); err != nil {
		t.Fatalf("写入表单文件失败: %v", err)
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatalf("写入表单字段失败: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("关闭 multipart 失败: %v", err)
	}
	return buf.Bytes(), mw.FormDataContentType()
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	h, _, _ := newTestEnv(t)
	return h
}

// newTestEnv 返回测试用的 handler 以及底层的配置与存储，便于直接构造数据。
func newTestEnv(t *testing.T) (http.Handler, *config.Store, *store.Store) {
	t.Helper()
	cfg, err := config.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开配置失败: %v", err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	assets := fstest.MapFS{
		"index.html":       {Data: []byte("<!DOCTYPE html><html><body>ok</body></html>")},
		"static/app.js":    {Data: []byte("// app")},
		"static/style.css": {Data: []byte("/* style */")},
	}
	svc := &service.Service{Store: st}
	if err := ops.Init(st.DB()); err != nil {
		t.Fatalf("初始化运维中心失败: %v", err)
	}
	srv := New(cfg, svc, st, assets)
	srv.SetOpsDB(st.DB())
	srv.SetEngine(ops.NewEngine(st.DB(), cfg, svc, t.TempDir()))
	srv.RebuildMeta()
	return srv.Handler(), cfg, st
}

func do(h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAccountsCRUDWithGroup(t *testing.T) {
	h := newTestHandler(t)

	w := do(h, http.MethodPost, "/api/accounts", `{"label":"A","username":"u1","token":"ghp_x","group":"工作"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("创建失败: %d %s", w.Code, w.Body.String())
	}
	var acc map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &acc)
	if acc["group"] != "工作" {
		t.Fatalf("group 未保存: %v", acc)
	}
	if strings.Contains(w.Body.String(), "ghp_x") {
		t.Fatal("响应不应包含明文 token")
	}

	w = do(h, http.MethodGet, "/api/accounts", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("列表失败: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "工作") {
		t.Fatal("列表应包含分组")
	}

	w = do(h, http.MethodDelete, "/api/accounts/1", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("删除失败: %d", w.Code)
	}
	w = do(h, http.MethodGet, "/api/accounts", "", nil)
	if strings.Contains(w.Body.String(), "u1") {
		t.Fatal("删除后列表不应包含 u1")
	}
}

func TestCSRFBlocked(t *testing.T) {
	h := newTestHandler(t)
	w := do(h, http.MethodPost, "/api/accounts", `{"username":"u","token":"t"}`, map[string]string{
		"Sec-Fetch-Site": "cross-site",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("跨站写操作应被拒绝，得到 %d", w.Code)
	}
}

func TestEncryptedExportImport(t *testing.T) {
	h := newTestHandler(t)
	if w := do(h, http.MethodPost, "/api/accounts", `{"label":"A","username":"u1","token":"ghp_secret","group":"g"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("准备账号失败: %d", w.Code)
	}

	// 加密导出
	w := do(h, http.MethodPost, "/api/accounts/export", `{"password":"pw"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("导出失败: %d", w.Code)
	}
	var enc map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &enc)
	if enc["encrypted"] != true {
		t.Fatal("应返回加密结构")
	}
	if strings.Contains(w.Body.String(), "ghp_secret") {
		t.Fatal("加密备份不应包含明文 token")
	}

	// 错误密码
	enc["password"] = "wrong"
	b, _ := json.Marshal(enc)
	w = do(h, http.MethodPost, "/api/accounts/import", string(b), nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("错误密码应返回 400，得到 %d", w.Code)
	}

	// 正确密码
	enc["password"] = "pw"
	b, _ = json.Marshal(enc)
	w = do(h, http.MethodPost, "/api/accounts/import", string(b), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("正确密码应返回 200，得到 %d: %s", w.Code, w.Body.String())
	}
	var res map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &res)
	if res["updated"] != float64(1) {
		t.Fatalf("应更新 1 个账号: %v", res)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	h := newTestHandler(t)
	w := do(h, http.MethodGet, "/api/search?q=", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("搜索失败: %d", w.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out["total"] != float64(0) {
		t.Fatalf("空查询应返回 0 条: %v", out)
	}
}

func TestVersionAndStatic(t *testing.T) {
	h := newTestHandler(t)

	w := do(h, http.MethodGet, "/api/version", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("版本接口失败: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), Version) {
		t.Fatalf("版本号不匹配: %s", w.Body.String())
	}

	if w := do(h, http.MethodGet, "/", "", nil); w.Code != http.StatusOK {
		t.Fatalf("首页失败: %d", w.Code)
	}
	if w := do(h, http.MethodGet, "/static/app.js", "", nil); w.Code != http.StatusOK {
		t.Fatalf("静态资源失败: %d", w.Code)
	}
	if w := do(h, http.MethodGet, "/static/style.css", "", nil); w.Code != http.StatusOK {
		t.Fatalf("样式失败: %d", w.Code)
	}
}

func Test404s(t *testing.T) {
	h := newTestHandler(t)
	for _, path := range []string{"/api/status/999", "/api/history/999"} {
		if w := do(h, http.MethodGet, path, "", nil); w.Code != http.StatusNotFound {
			t.Fatalf("%s 应返回 404，得到 %d", path, w.Code)
		}
	}
	// 不存在的方法/路径组合返回 405（Go ServeMux 语义）
	if w := do(h, http.MethodGet, "/api/accounts/1", "", nil); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/accounts/{id} 应返回 405，得到 %d", w.Code)
	}
}

func TestBatchTagAndGroup(t *testing.T) {
	h, cfg, _ := newTestEnv(t)
	if w := do(h, http.MethodPost, "/api/accounts", `{"username":"u1","token":"t1","group":"工作"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("建号失败: %d", w.Code)
	}
	do(h, http.MethodPost, "/api/accounts", `{"username":"u2","token":"t2","group":"个人"}`, nil)

	w := do(h, http.MethodPost, "/api/accounts/batch-tag", `{"ids":[1,2],"tags":["核心","ci"]}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("批量打标签失败: %d %s", w.Code, w.Body.String())
	}
	accounts, _ := cfg.Accounts()
	for _, a := range accounts {
		if len(a.Tags) != 2 {
			t.Fatalf("标签未保存: %v", a.Tags)
		}
	}

	w = do(h, http.MethodPost, "/api/accounts/batch-group", `{"ids":[1,2],"group":"测试"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("批量分组失败: %d %s", w.Code, w.Body.String())
	}
	accounts, _ = cfg.Accounts()
	for _, a := range accounts {
		if a.Group != "测试" {
			t.Fatalf("分组未保存: %q", a.Group)
		}
	}

	// 幂等：重复打同一个标签不应重复
	do(h, http.MethodPost, "/api/accounts/batch-tag", `{"ids":[1],"tags":["核心"]}`, nil)
	accounts, _ = cfg.Accounts()
	if len(accounts[0].Tags) != 2 {
		t.Fatalf("标签重复: %v", accounts[0].Tags)
	}

	if w := do(h, http.MethodPost, "/api/accounts/batch-tag", `{"ids":[1],"tags":[]}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("空标签应 400，得到 %d", w.Code)
	}
	if w := do(h, http.MethodPost, "/api/accounts/batch-group", `{"ids":[1],"group":""}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("空分组应 400，得到 %d", w.Code)
	}
}

func TestBatchToggleSync(t *testing.T) {
	h, _, st := newTestEnv(t)
	do(h, http.MethodPost, "/api/accounts", `{"username":"u1","token":"t1"}`, nil)
	// 刻意不预先创建 sync_state：暂停操作应自建状态行，否则会静默失效
	w := do(h, http.MethodPost, "/api/accounts/batch-toggle-sync", `{"ids":[1],"enabled":false}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("暂停同步失败: %d %s", w.Code, w.Body.String())
	}
	state, _ := ops.GetSyncState(st.DB(), 1)
	if state == nil || state.Enabled() {
		t.Fatalf("应已暂停同步并创建状态行: %+v", state)
	}

	w = do(h, http.MethodGet, "/api/accounts?pageSize=10", "", nil)
	if !strings.Contains(w.Body.String(), `"sync_enabled":false`) {
		t.Fatalf("列表未反映暂停状态: %s", w.Body.String())
	}

	do(h, http.MethodPost, "/api/accounts/batch-toggle-sync", `{"ids":[1],"enabled":true}`, nil)
	state, _ = ops.GetSyncState(st.DB(), 1)
	if state == nil || !state.Enabled() {
		t.Fatalf("应已启用同步: %+v", state)
	}
}

func TestImportDocx(t *testing.T) {
	h, cfg, _ := newTestEnv(t)
	doc := buildDocx(t, []string{
		"账号：alice",
		"ghp_" + strings.Repeat("a", 36),
	})

	// 预览：只回脱敏 Token，且不得泄露明文
	body, ctype := multipartBody(t, doc, nil)
	w := do(h, http.MethodPost, "/api/accounts/import-docx?preview=true", string(body), map[string]string{
		"Content-Type": ctype,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("预览失败: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), strings.Repeat("a", 36)) {
		t.Fatal("预览响应泄露了明文 Token")
	}
	var preview struct {
		Total      int `json:"total"`
		Candidates []struct {
			Index       int    `json:"index"`
			Username    string `json:"username"`
			TokenMasked string `json:"token_masked"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &preview); err != nil {
		t.Fatalf("解析预览响应失败: %v", err)
	}
	if preview.Total != 1 || preview.Candidates[0].Username != "alice" {
		t.Fatalf("预览结果不正确: %+v", preview)
	}

	// 导入：带显示名覆盖（Token 无效也允许显式导入）
	body, ctype = multipartBody(t, doc, map[string]string{"overrides": `{"0":{"label":"工作号"}}`})
	w = do(h, http.MethodPost, "/api/accounts/import-docx?preview=false&indices=0", string(body), map[string]string{
		"Content-Type": ctype,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("导入失败: %d %s", w.Code, w.Body.String())
	}
	accounts, _ := cfg.Accounts()
	if len(accounts) != 1 {
		t.Fatalf("应导入 1 个账号: %+v", accounts)
	}
	if accounts[0].Username != "alice" || accounts[0].Label != "工作号" {
		t.Fatalf("导入的账号信息不正确: %+v", accounts[0])
	}
}
