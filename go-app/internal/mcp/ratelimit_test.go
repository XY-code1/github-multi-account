package mcp

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github-multi-account/internal/config"
	"github-multi-account/internal/ops"
	"github-multi-account/internal/service"
	"github-multi-account/internal/store"
)

func TestTokenBucketExhausted(t *testing.T) {
	b := newTokenBucket(2, 0.0001)
	if ok, _ := b.take(); !ok {
		t.Fatal("第 1 次应取到令牌")
	}
	if ok, _ := b.take(); !ok {
		t.Fatal("第 2 次应取到令牌")
	}
	ok, wait := b.take()
	if ok {
		t.Fatal("桶空后应拒绝")
	}
	if wait <= 0 {
		t.Fatal("应返回需要等待的秒数")
	}
}

func TestLimiterHeavyToolsUseStrictBucket(t *testing.T) {
	base := t.TempDir()
	writeSettings(t, base, "  heavy_capacity: 1\n  heavy_refill_per_second: 0.0001\n")
	l := newLimiter(base)

	if ok, _, _ := l.allow("sync_all"); !ok {
		t.Fatal("重量工具首次调用应放行")
	}
	ok, wait, desc := l.allow("sync_all")
	if ok {
		t.Fatal("重量工具第二次调用应被限流")
	}
	if wait <= 0 || !strings.Contains(desc, "重量工具桶") {
		t.Fatalf("应返回等待秒数与重量桶说明: wait=%v desc=%s", wait, desc)
	}
	// 普通工具不受重量桶影响
	if ok, _, _ := l.allow("get_summary"); !ok {
		t.Fatal("普通工具不应被重量桶限流")
	}
}

func TestLimiterDisabled(t *testing.T) {
	base := t.TempDir()
	writeSettings(t, base, "  enabled: false\n")
	l := newLimiter(base)
	for i := 0; i < 20; i++ {
		if ok, _, _ := l.allow("sync_all"); !ok {
			t.Fatal("关闭限流后不应拒绝任何调用")
		}
	}
}

func TestLimiterHotReload(t *testing.T) {
	base := t.TempDir()
	// 用默认重量工具集合（含 sync_all），只把容量放宽到 5
	writeSettings(t, base, "  heavy_capacity: 5\n  heavy_refill_per_second: 0.0001\n")
	l := newLimiter(base)
	for i := 0; i < 5; i++ {
		if ok, _, _ := l.allow("sync_all"); !ok {
			t.Fatalf("容量 5 时第 %d 次应放行", i+1)
		}
	}
	// 收紧到 1 并强制刷新（修改 mtime）；已消耗的令牌不会被热加载重置
	writeSettings(t, base, "  heavy_capacity: 1\n  heavy_refill_per_second: 0.0001\n  heavy_tools: [sync_all]\n")
	l.reload()
	if ok, _, _ := l.allow("sync_all"); ok {
		t.Fatal("热加载收紧后应拒绝")
	}
	// 清空重量工具集合后，sync_all 回落到普通桶
	writeSettings(t, base, "  heavy_tools: []\n  bucket_capacity: 8\n")
	l.reload()
	if ok, _, _ := l.allow("sync_all"); !ok {
		t.Fatal("清空重量工具后应使用普通桶放行")
	}
}

func TestMCPToolCallIsRateLimited(t *testing.T) {
	base := t.TempDir()
	writeSettings(t, base,
		"  heavy_capacity: 1\n  heavy_tools: [get_summary]\n  heavy_refill_per_second: 0.0001\n")
	s := newTestServerWithDir(t, base)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_summary","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_summary","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"mcp_status","arguments":{}}}`,
	}, "\n")

	var out bytes.Buffer
	if err := s.serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("serve 失败: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("应有 3 个响应，得到 %d:\n%s", len(lines), out.String())
	}
	if strings.Contains(lines[0], "isError") {
		t.Fatalf("首次调用不应被限流: %s", lines[0])
	}
	if !strings.Contains(lines[1], "限流") || !strings.Contains(lines[1], "秒") {
		t.Fatalf("第二次调用应返回限流提示: %s", lines[1])
	}
	if !strings.Contains(lines[2], "enabled") {
		t.Fatalf("mcp_status 应返回限流状态: %s", lines[2])
	}
}

func writeSettings(t *testing.T, dir, mcpBody string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("创建配置目录失败: %v", err)
	}
	body := "mcp:\n" + mcpBody + "\n"
	if err := os.WriteFile(filepath.Join(dir, "settings.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("写入 settings.yaml 失败: %v", err)
	}
	// 确保 mtime 变化可被 reload 感知（文件系统 mtime 精度可能只到秒，
	// 因此每次写入都显式设置一个递增的未来时间）
	settingsWrites++
	future := time.Now().Add(time.Duration(2+settingsWrites) * time.Second)
	_ = os.Chtimes(filepath.Join(dir, "settings.yaml"), future, future)
}

// settingsWrites 记录测试中写 settings.yaml 的次数，用于递增 mtime。
var settingsWrites int

// newTestServerWithDir 用指定配置目录创建测试 Server（settings.yaml 直接放在 dir 下）。
func newTestServerWithDir(t *testing.T, dir string) *Server {
	t.Helper()
	cfg, err := config.Open(filepath.Join(dir, "config"))
	if err != nil {
		t.Fatalf("打开配置失败: %v", err)
	}
	st, err := store.Open(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := ops.Init(st.DB()); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if _, err := cfg.Add(config.Account{Label: "u", Username: "u1", Token: "t", Type: "personal"}); err != nil {
		t.Fatalf("建账号失败: %v", err)
	}
	s := &Server{
		cfg:     cfg,
		db:      st.DB(),
		svc:     &service.Service{Store: st},
		engine:  ops.NewEngine(st.DB(), cfg, &service.Service{Store: st}, filepath.Join(dir, "config")),
		limiter: newLimiter(dir),
	}
	s.registerTools()
	return s
}
