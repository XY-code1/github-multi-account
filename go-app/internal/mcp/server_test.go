package mcp

import (
	"bytes"
	"strings"
	"testing"

	"github-multi-account/internal/config"
	"github-multi-account/internal/ops"
	"github-multi-account/internal/service"
	"github-multi-account/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	base := t.TempDir()
	cfg, err := config.Open(base + "/config")
	if err != nil {
		t.Fatalf("打开配置失败: %v", err)
	}
	st, err := store.Open(base + "/data")
	if err != nil {
		t.Fatalf("打开存储失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := ops.Init(st.DB()); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if _, err := cfg.Add(config.Account{Label: "工作号", Username: "u1", Token: "t", Type: "personal"}); err != nil {
		t.Fatalf("建账号失败: %v", err)
	}
	s := &Server{
		cfg:    cfg,
		db:     st.DB(),
		svc:    &service.Service{Store: st},
		engine: ops.NewEngine(st.DB(), cfg, &service.Service{Store: st}, base+"/config"),
	}
	s.registerTools()
	return s
}

func TestServeProtocol(t *testing.T) {
	s := newTestServer(t)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_summary","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"list_accounts","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"nope","arguments":{}}}`,
	}, "\n")

	var out bytes.Buffer
	if err := s.serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("serve 失败: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	// notifications/initialized 不产生响应，共 6 个
	if len(lines) != 6 {
		t.Fatalf("应有 6 个响应，得到 %d:\n%s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], `"serverInfo"`) || !strings.Contains(lines[0], `"name":"github-multi-account"`) {
		t.Fatalf("initialize 响应不正确: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"result":{}`) {
		t.Fatalf("ping 响应不正确: %s", lines[1])
	}
	if !strings.Contains(lines[2], `"name":"sync_all"`) || !strings.Contains(lines[2], `"name":"get_todos"`) {
		t.Fatalf("tools/list 响应不正确: %s", lines[2])
	}
	if !strings.Contains(lines[3], "quota_low") {
		t.Fatalf("get_summary 响应不正确: %s", lines[3])
	}
	if !strings.Contains(lines[4], "username") {
		t.Fatalf("list_accounts 响应不正确: %s", lines[4])
	}
	if !strings.Contains(lines[5], "Unknown tool") {
		t.Fatalf("未知工具应报错: %s", lines[5])
	}
}

func TestToolBatchTag(t *testing.T) {
	s := newTestServer(t)

	input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"batch_tag","arguments":{"ids":[1],"tags":["核心"]}}}` + "\n"
	var out bytes.Buffer
	if err := s.serve(strings.NewReader(input), &out); err != nil {
		t.Fatalf("serve 失败: %v", err)
	}
	if !strings.Contains(out.String(), "updated") {
		t.Fatalf("批量打标签失败: %s", out.String())
	}
	accounts, _ := s.cfg.Accounts()
	if len(accounts[0].Tags) != 1 || accounts[0].Tags[0] != "核心" {
		t.Fatalf("标签未写入: %+v", accounts[0].Tags)
	}
}
