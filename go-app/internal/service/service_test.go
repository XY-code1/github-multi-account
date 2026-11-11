package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github-multi-account/internal/config"
	"github-multi-account/internal/github"
)

type fakeGitHub struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []string
}

// newFake 启动一个假 GitHub API；failPrefix 非空时，匹配前缀的路径返回 500。
func newFake(t *testing.T, failPrefix string) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = append(f.calls, r.URL.Path)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if failPrefix != "" && strings.Contains(r.URL.Path, failPrefix) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
			return
		}
		switch {
		case r.URL.Path == "/user":
			_, _ = w.Write([]byte(`{"login":"u","name":"U","avatar_url":"http://a","followers":100,"following":50,"public_repos":20}`))
		case strings.Contains(r.URL.Path, "/repos"):
			if r.URL.Query().Get("page") == "1" || r.URL.Query().Get("page") == "" {
				_, _ = w.Write([]byte(`[{"name":"r1","html_url":"http://r1","stargazers_count":10,"forks_count":1,"language":"Go","private":false,"updated_at":"2026-01-01T00:00:00Z"}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case strings.Contains(r.URL.Path, "/followers"), strings.Contains(r.URL.Path, "/following"):
			_, _ = w.Write([]byte(`[{"login":"f1","avatar_url":"http://f1","html_url":"http://f1"}]`))
		case strings.Contains(r.URL.Path, "/search/issues"):
			_, _ = w.Write([]byte(`{"total_count":7,"items":[{"title":"PR1","html_url":"http://p","repository_url":"http://repo/o/r","number":1,"user":{"login":"x"},"labels":[{"name":"bug"}],"created_at":"2026-01-01T00:00:00Z"}]}`))
		case strings.Contains(r.URL.Path, "/rate_limit"):
			_, _ = w.Write([]byte(`{"resources":{"core":{"remaining":100,"limit":5000}}}`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	github.BaseURL = f.srv.URL
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) hasCalled(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

var testAcc = config.Account{ID: 1, Label: "A", Username: "u", Token: "t"}

func TestSummaryLightweight(t *testing.T) {
	f := newFake(t, "")
	svc := &Service{}
	st := svc.Summary(testAcc)

	if st.Status != "active" {
		t.Fatalf("状态应为 active，得到 %s（错误：%s）", st.Status, st.Error)
	}
	if st.FollowersCount != 100 || st.FollowingCount != 50 || st.PublicRepos != 20 {
		t.Fatalf("对比指标错误: %d/%d/%d", st.FollowersCount, st.FollowingCount, st.PublicRepos)
	}
	if st.ReviewPRsCount != 7 {
		t.Fatalf("计数应取自 total_count=7，得到 %d", st.ReviewPRsCount)
	}
	// 汇总不应调用重型接口
	for _, heavy := range []string{"/repos", "/followers", "/following", "/received_events", "/rate_limit"} {
		if f.hasCalled(heavy) {
			t.Fatalf("汇总不应调用重型接口: %s", heavy)
		}
	}
}

func TestSummarySurvivesPartialFailure(t *testing.T) {
	newFake(t, "/search/issues")
	svc := &Service{}
	st := svc.Summary(testAcc)

	if st.Status != "active" {
		t.Fatalf("部分失败不应拖垮整个账号，状态=%s 错误=%s", st.Status, st.Error)
	}
	if len(st.Warnings) == 0 {
		t.Fatal("应记录 warnings")
	}
	if st.ReviewPRsCount != 0 {
		t.Fatal("失败的字段应降级为 0")
	}
}

func TestDetailSurvivesPartialFailure(t *testing.T) {
	newFake(t, "/following")
	svc := &Service{}
	st := svc.Detail(testAcc, false)

	if st.Status != "active" {
		t.Fatalf("部分失败不应拖垮整个账号，状态=%s 错误=%s", st.Status, st.Error)
	}
	if len(st.Warnings) == 0 {
		t.Fatal("应记录 warnings")
	}
	if st.FollowersCount != 100 {
		t.Fatalf("其余字段应保留，followers=%d", st.FollowersCount)
	}
	if len(st.FollowerList) == 0 {
		t.Fatal("粉丝列表应保留")
	}
	if len(st.FollowingList) != 0 {
		t.Fatal("失败的关注列表应降级为空数组")
	}
}

func TestDetailComputesStars(t *testing.T) {
	newFake(t, "")
	svc := &Service{}
	st := svc.Detail(testAcc, false)

	if st.TotalStars != 10 {
		t.Fatalf("总 Star 应为 10，得到 %d", st.TotalStars)
	}
	if len(st.TopRepos) != 1 || st.TopRepos[0].Name != "r1" {
		t.Fatalf("Star 排行错误: %+v", st.TopRepos)
	}
	if len(st.StarDistribution) != 5 {
		t.Fatal("Star 分布应有 5 段")
	}
}

func TestVerify(t *testing.T) {
	newFake(t, "")
	status, user, msg := Verify("u", "t")
	if status != "active" || user == nil || msg != "" {
		t.Fatalf("验证应成功: status=%s user=%v msg=%s", status, user, msg)
	}
	status, _, msg = Verify("u", "")
	if status != "invalid" {
		t.Fatalf("空 token 应 invalid: %s", status)
	}
}
