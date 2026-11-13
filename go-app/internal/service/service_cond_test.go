package service

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github-multi-account/internal/config"
	"github-multi-account/internal/github"
)

// TestSyncSummaryCond 验证增量同步：带 ETag 时未变化的端点返回 304，
// 直接用上一次的结果，不重复拉取。
func TestSyncSummaryCond(t *testing.T) {
	const etag = `W/"abc123"`

	var mu sync.Mutex
	counts := map[string]int{}
	modified := map[string]int{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		counts[r.URL.Path]++
		mu.Unlock()
		if r.Header.Get("If-None-Match") == etag {
			mu.Lock()
			modified[r.URL.Path]++
			mu.Unlock()
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"login":"u","followers":1,"following":1,"public_repos":1}`))
		case "/search/issues":
			_, _ = w.Write([]byte(`{"total_count":3,"items":[{"title":"x"}]}`))
		case "/users/u/received_events":
			_, _ = w.Write([]byte(`[{"type":"PushEvent"}]`))
		case "/rate_limit":
			_, _ = w.Write([]byte(`{"resources":{"core":{"remaining":100,"limit":5000}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	old := github.BaseURL
	github.BaseURL = srv.URL
	defer func() { github.BaseURL = old }()

	acc := config.Account{ID: 1, Username: "u", Token: "t"}
	svc := &Service{}

	// 首次：没有 ETag，全部真实请求
	etags := map[string]string{}
	first := svc.SyncSummaryCond(acc, etags, nil)
	if first.ReviewPRsCount != 3 {
		t.Fatalf("首次应取到 3 条，得到 %d", first.ReviewPRsCount)
	}
	if etags["review_prs"] != etag || etags["events"] != etag {
		t.Fatalf("ETag 未写回: %+v", etags)
	}

	// 第二次：带 ETag，search 与 events 命中 304
	counts = map[string]int{}
	modified = map[string]int{}
	second := svc.SyncSummaryCond(acc, etags, first)
	if second.ReviewPRsCount != 3 {
		t.Fatalf("304 后应保留旧计数 3，得到 %d", second.ReviewPRsCount)
	}
	if len(second.Events) != 1 || second.Events[0].Type != "PushEvent" {
		t.Fatalf("304 后应保留旧动态: %+v", second.Events)
	}

	mu.Lock()
	defer mu.Unlock()
	if modified["/search/issues"] == 0 {
		t.Fatal("搜索端点应命中 304（条件请求未生效）")
	}
	if modified["/users/u/received_events"] == 0 {
		t.Fatal("动态端点应命中 304")
	}
	// /user 与 /rate_limit 不参与条件请求
	if counts["/user"] != 1 || counts["/rate_limit"] != 1 {
		t.Fatalf("未参与条件请求的端点应各请求 1 次: %+v", counts)
	}
}
