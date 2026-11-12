package server

import (
	"bytes"
	"encoding/json"
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

func newTestHandler(t *testing.T) http.Handler {
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
	return srv.Handler()
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
