package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newMockClient 返回一个指向 httptest 服务器的客户端，并把该服务器的请求记录到 *calls。
func newMockClient(t *testing.T, handler http.HandlerFunc, calls *[]string) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = old })
	return New("test-token")
}

func record(calls *[]string, r *http.Request) {
	if calls != nil {
		*calls = append(*calls, r.Method+" "+r.URL.Path)
	}
}

func rateLimitHandler(remaining int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"resources": map[string]any{
				"core": map[string]any{"limit": 5000, "remaining": remaining, "reset": time.Now().Add(time.Minute).Unix()},
			},
		})
	}
}

func TestCheckQuotaRejectsLowQuota(t *testing.T) {
	var calls []string
	c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		record(&calls, r)
		rateLimitHandler(10)(w, r)
	}, &calls)

	_, err := c.StarRepo("o", "r")
	if err == nil {
		t.Fatal("配额不足时应报错")
	}
	if got := err.(*Error).Status; got != 403 {
		t.Fatalf("错误码应为 403，得到 %d", got)
	}
	if len(calls) != 1 || calls[0] != "GET /rate_limit" {
		t.Fatalf("配额不足时不应发起写请求，实际调用: %v", calls)
	}
}

func TestStarRepoAlready(t *testing.T) {
	var calls []string
	c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		record(&calls, r)
		switch {
		case r.URL.Path == "/rate_limit":
			rateLimitHandler(5000)(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/user/starred/o/r":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, &calls)

	already, err := c.StarRepo("o", "r")
	if err != nil {
		t.Fatalf("StarRepo 失败: %v", err)
	}
	if !already {
		t.Fatal("已 star 时应返回 already=true")
	}
	for _, call := range calls {
		if call == "PUT /user/starred/o/r" {
			t.Fatal("已 star 时不应重复 PUT")
		}
	}
}

func TestStarRepoCreated(t *testing.T) {
	var calls []string
	c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		record(&calls, r)
		switch {
		case r.URL.Path == "/rate_limit":
			rateLimitHandler(5000)(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/user/starred/o/r":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut && r.URL.Path == "/user/starred/o/r":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, &calls)

	already, err := c.StarRepo("o", "r")
	if err != nil {
		t.Fatalf("StarRepo 失败: %v", err)
	}
	if already {
		t.Fatal("首次 star 应返回 already=false")
	}
}

func TestFollowIdempotent(t *testing.T) {
	var putSeen bool
	c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rate_limit":
			rateLimitHandler(5000)(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/user/following/alice":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && r.URL.Path == "/user/following/alice":
			putSeen = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, nil)

	already, err := c.Follow("alice")
	if err != nil {
		t.Fatalf("Follow 失败: %v", err)
	}
	if !already || putSeen {
		t.Fatal("已关注时不应再 PUT")
	}
}

func TestRateLimitHint(t *testing.T) {
	h := http.Header{}
	h.Set("X-RateLimit-Remaining", "3")
	h.Set("Retry-After", "30")
	if got := rateLimitHint(h); got == "" {
		t.Fatal("应解析出限流提示")
	}
}

func TestCreatePRCreated(t *testing.T) {
	var calls []string
	newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		record(&calls, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "me"})
		case r.URL.Path == "/rate_limit":
			rateLimitHandler(5000)(w, r)
		case r.URL.Path == "/repos/o/r":
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "html_url": "https://github.com/o/r/pull/7", "title": "t"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, &calls)

	result, err := CreatePR(context.Background(), AccountRef{ID: 1, Username: "me", Token: "t"},
		"o/r", "标题", CreatePROptions{Head: "feature"})
	if err != nil {
		t.Fatalf("CreatePR 失败: %v", err)
	}
	if result["action"] != "created" {
		t.Fatalf("应创建 PR，得到 %v", result["action"])
	}
	if result["url"] != "https://github.com/o/r/pull/7" {
		t.Fatalf("URL 不正确: %v", result["url"])
	}
}

func TestCreatePRExisting(t *testing.T) {
	var posted bool
	newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "me"})
		case r.URL.Path == "/rate_limit":
			rateLimitHandler(5000)(w, r)
		case r.URL.Path == "/repos/o/r":
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 3, "html_url": "u3", "title": "old"}})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls":
			posted = true
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 8})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, nil)

	result, err := CreatePR(context.Background(), AccountRef{ID: 1, Username: "me", Token: "t"},
		"o/r", "标题", CreatePROptions{Head: "feature"})
	if err != nil {
		t.Fatalf("CreatePR 失败: %v", err)
	}
	if result["action"] != "existing" || posted {
		t.Fatal("已存在同源同目标的开 PR 时应直接返回，不再创建")
	}
}

func TestCreatePRFork(t *testing.T) {
	newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user":
			_ = json.NewEncoder(w).Encode(map[string]any{"login": "me"})
		case r.URL.Path == "/rate_limit":
			rateLimitHandler(5000)(w, r)
		case r.URL.Path == "/repos/o/r":
			_ = json.NewEncoder(w).Encode(map[string]any{"default_branch": "main"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/me/r":
			_ = json.NewEncoder(w).Encode(map[string]any{"fork": true})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/pulls":
			_ = json.NewEncoder(w).Encode([]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/pulls":
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 9, "html_url": "u9"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, nil)

	// 已 fork 时不应调用 POST /forks
	result, err := CreatePR(context.Background(), AccountRef{ID: 1, Username: "me", Token: "t"},		"o/r", "标题", CreatePROptions{Fork: true, Branch: "feature"})
	if err != nil {
		t.Fatalf("CreatePR(fork) 失败: %v", err)
	}
	if result["action"] != "created" {
		t.Fatalf("应创建 PR，得到 %v", result["action"])
	}
}

func TestSplitRepo(t *testing.T) {
	if _, _, err := SplitRepo("bad"); err == nil {
		t.Fatal("非法 repo 应报错")
	}
	owner, name, err := SplitRepo("o/r")
	if err != nil || owner != "o" || name != "r" {
		t.Fatalf("解析失败: %s %s %v", owner, name, err)
	}
}

func TestPutFileKeepsPathSegments(t *testing.T) {
	var putPath string
	c := newMockClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/contents/docs/a.md":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut:
			putPath = r.URL.Path
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}, nil)
	if err := c.PutFile("o", "r", "docs/a.md", "hi", "main", "msg"); err != nil {
		t.Fatalf("PutFile 失败: %v", err)
	}
	if putPath != "/repos/o/r/contents/docs/a.md" {
		t.Fatalf("路径分段被破坏: %s", putPath)
	}
}
