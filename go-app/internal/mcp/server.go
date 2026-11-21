// Package mcp 实现一个最小化的 MCP（Model Context Protocol）服务端，
// 通过 stdio 暴露运维中心能力，让 Claude / Cursor 等客户端直接调用。
//
// 用法：GitHub仪表盘.exe --mcp
// 然后在 MCP 客户端配置里把它指向该 exe 即可。
package mcp

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"time"

	"github-multi-account/internal/config"
	"github-multi-account/internal/github"
	"github-multi-account/internal/ops"
	"github-multi-account/internal/service"
	"github-multi-account/internal/store"
)

// Version 与 server.Version 保持一致。
const Version = "1.3.0"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type tool struct {
	name        string
	description string
	schema      map[string]any
	handle      func(ctx context.Context, args map[string]any) (string, error)
}

// Server 是 MCP 服务端。
type Server struct {
	cfg    *config.Store
	db     *sql.DB
	svc    *service.Service
	engine *ops.Engine
	tools  []tool
}

// Run 启动 stdio MCP 服务（阻塞）。
func Run(baseDir string) error {
	// 协议走 stdout，日志必须走 stderr，否则会污染 JSON-RPC
	log.SetOutput(os.Stderr)

	cfg, err := config.Open(configDir(baseDir))
	if err != nil {
		return err
	}
	st, err := store.Open(dataDir(baseDir))
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if err := ops.Init(st.DB()); err != nil {
		return err
	}

	s := &Server{
		cfg:    cfg,
		db:     st.DB(),
		svc:    &service.Service{Store: st},
		engine: ops.NewEngine(st.DB(), cfg, &service.Service{Store: st}, configDir(baseDir)),
	}
	s.registerTools()
	return s.serve(os.Stdin, os.Stdout)
}

func configDir(base string) string { return base + string(os.PathSeparator) + "config" }
func dataDir(base string) string   { return base + string(os.PathSeparator) + "data" }

func (s *Server) serve(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	enc := json.NewEncoder(out)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("0"), Error: &rpcError{-32700, "Parse error"}})
			continue
		}
		resp := s.handle(req)
		if resp != nil {
			_ = enc.Encode(resp)
		}
	}
	return scanner.Err()
}

func (s *Server) handle(req request) *response {
	switch req.Method {
	case "initialize":
		return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "github-multi-account", "version": Version},
		}}
	case "notifications/initialized", "notifications/cancelled":
		return nil
	case "ping":
		return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		tools := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			tools = append(tools, map[string]any{
				"name":        t.name,
				"description": t.description,
				"inputSchema": t.schema,
		})
		}
		return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": tools}}
	case "tools/call":
		return s.callTool(req)
	default:
		if req.ID == nil {
			return nil
		}
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32601, "Method not found: " + req.Method}}
	}
}

func (s *Server) callTool(req request) *response {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32602, "Invalid params"}}
	}
	for _, t := range s.tools {
		if t.name != params.Name {
			continue
		}
		text, err := t.handle(context.Background(), params.Arguments)
		if err != nil {
			// 工具级错误用 isError 表达，避免整个请求失败
			return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
				"content": []map[string]any{{"type": "text", "text": "错误: " + err.Error()}},
				"isError": true,
			}}
		}
		return &response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
		}}
	}
	return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{-32602, "Unknown tool: " + params.Name}}
}

func (s *Server) registerTools() {
	s.tools = []tool{
		{
			name:        "list_accounts",
			description: "列出全部账号（支持按状态/类型/分组/关键字筛选与分页）",
			schema: objectSchema(map[string]any{
				"status": "账号状态：active/invalid/error/sync_failed",
				"type":   "账号类型：personal/bot/org",
				"group":  "分组名",
				"query":  "搜索用户名/别名/标签/备注",
				"page":   "页码，默认 1",
				"pageSize": "每页条数，默认 50",
			}),
			handle: s.handleListAccounts,
		},
		{
			name:        "get_todos",
			description: "获取统一待办（按优先级排序，可按类型/优先级筛选）",
			schema: objectSchema(map[string]any{
				"priority": "high/medium/low",
				"type":     "credential/quota/quota_capped/cross_account/review_pr/my_pr/issue/sync_failed/sync_stale",
				"accountId": "只看某个账号",
			}),
			handle: s.handleGetTodos,
		},
		{
			name:        "get_summary",
			description: "获取账号总览统计（健康数、风险数、待办数等）",
			schema:      objectSchema(map[string]any{}),
			handle:      s.handleGetSummary,
		},
		{
			name:        "get_account_detail",
			description: "获取单个账号的完整详情（PR/Issue/动态/仓库/粉丝）",
			schema: objectSchema(map[string]any{
				"accountId": "账号 ID（必填）",
			}),
			handle: s.handleGetAccountDetail,
		},
		{
			name:        "sync_account",
			description: "立即同步单个账号",
			schema: objectSchema(map[string]any{
				"accountId": "账号 ID（必填）",
			}),
			handle: s.handleSyncAccount,
		},
		{
			name:        "sync_all",
			description: "立即同步全部账号",
			schema:      objectSchema(map[string]any{}),
			handle:      s.handleSyncAll,
		},
		{
			name:        "batch_tag",
			description: "批量给账号添加标签",
			schema: objectSchema(map[string]any{
				"ids":  "账号 ID 列表（必填）",
				"tags": "标签列表（必填）",
			}),
			handle: s.handleBatchTag,
		},
		{
			name:        "get_audit",
			description: "查询审计日志",
			schema: objectSchema(map[string]any{
				"limit": "条数，默认 20",
			}),
			handle: s.handleGetAudit,
		},
		{
			name:        "star_repo",
			description: "以指定账号 Star 仓库（幂等，重复执行无副作用）",
			schema: typedSchema(map[string]any{
				"accountId": "账号 ID（必填）",
				"owner":     "仓库所有者（必填）",
				"repo":      "仓库名（必填）",
			}, map[string]string{"accountId": "number"}),
			handle: s.handleStarRepo,
		},
		{
			name:        "follow_user",
			description: "以指定账号关注用户（幂等）",
			schema: typedSchema(map[string]any{
				"accountId": "账号 ID（必填）",
				"username":  "要关注的用户名（必填）",
			}, map[string]string{"accountId": "number"}),
			handle: s.handleFollowUser,
		},
		{
			name:        "create_pr",
			description: "以指定账号创建 Pull Request（支持同仓库分支与 fork 两种模式；已存在同源同目标的开 PR 时直接返回该 PR）",
			schema: typedSchema(map[string]any{
				"accountId":   "账号 ID（必填）",
				"repo":        "目标仓库 owner/repo（必填）",
				"title":       "PR 标题（必填）",
				"base":        "目标分支，默认取仓库默认分支",
				"head":        "源分支名（同仓库模式为该分支名；fork 模式为 fork 内分支名）",
				"body":        "PR 正文",
				"draft":       "是否创建为 Draft",
				"fork":        "为 true 时自动 fork 后用 login:branch 作为 head",
				"branch":      "需要提交文件时的目标分支（与 filePath/fileContent 一起用）",
				"filePath":    "要提交的文件路径（可选）",
				"fileContent": "文件内容（可选）",
			}, map[string]string{"accountId": "number", "draft": "boolean", "fork": "boolean"}),
			handle: s.handleCreatePR,
		},
	}
}

// typedSchema 生成带类型标注的 object schema（objectSchema 只支持 string）。
func typedSchema(props map[string]any, types map[string]string) map[string]any {
	properties := map[string]any{}
	for k, v := range props {
		t := types[k]
		if t == "" {
			t = "string"
		}
		properties[k] = map[string]any{"type": t, "description": fmt.Sprint(v)}
	}
	return map[string]any{"type": "object", "properties": properties, "required": []string{}}
}

func objectSchema(props map[string]any) map[string]any {
	properties := map[string]any{}
	required := []string{}
	for k, v := range props {
		properties[k] = map[string]any{"type": "string", "description": fmt.Sprint(v)}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required}
}

func textResult(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (s *Server) handleListAccounts(_ context.Context, args map[string]any) (string, error) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		return "", err
	}
	page, _ := args["page"].(float64)
	pageSize, _ := args["pageSize"].(float64)
	result, err := ops.QueryAccounts(s.db, accounts, ops.AccountQuery{
		Page:     int(page),
		PageSize: int(pageSize),
		Status:   strArg(args, "status"),
		Type:     strArg(args, "type"),
		Group:    strArg(args, "group"),
		Query:    strArg(args, "query"),
	})
	if err != nil {
		return "", err
	}
	return textResult(result)
}

func (s *Server) handleGetTodos(_ context.Context, args map[string]any) (string, error) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		return "", err
	}
	all, err := ops.BuildTasks(s.db, accounts)
	if err != nil {
		return "", err
	}
	priority := strArg(args, "priority")
	typeFilter := strArg(args, "type")
	accountFilter := strArg(args, "accountId")
	out := []ops.Task{}
	for _, t := range all {
		if priority != "" && t.Priority != priority {
			continue
		}
		if typeFilter != "" && t.Type != typeFilter {
			continue
		}
		if accountFilter != "" && strconv.Itoa(t.AccountID) != accountFilter {
			continue
		}
		out = append(out, t)
	}
	return textResult(map[string]any{"tasks": out, "total": len(out)})
}

func (s *Server) handleGetSummary(_ context.Context, _ map[string]any) (string, error) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		return "", err
	}
	summary, err := ops.BuildSummary(s.db, accounts)
	if err != nil {
		return "", err
	}
	return textResult(summary)
}

func (s *Server) findAccount(id int) (config.Account, error) {
	accounts, err := s.cfg.Accounts()
	if err != nil {
		return config.Account{}, err
	}
	for _, a := range accounts {
		if a.ID == id {
			return a, nil
		}
	}
	return config.Account{}, fmt.Errorf("账号 %d 不存在", id)
}

func (s *Server) handleGetAccountDetail(_ context.Context, args map[string]any) (string, error) {
	id := int(numArg(args, "accountId"))
	acc, err := s.findAccount(id)
	if err != nil {
		return "", err
	}
	return textResult(s.svc.Detail(acc, false))
}

func (s *Server) handleSyncAccount(_ context.Context, args map[string]any) (string, error) {
	id := int(numArg(args, "accountId"))
	acc, err := s.findAccount(id)
	if err != nil {
		return "", err
	}
	ok, status, reason := s.engine.SyncAccount(acc)
	ops.Audit(s.db, "accounts.sync", &acc.ID, acc.Username, "MCP 手动同步")
	return textResult(map[string]any{"id": acc.ID, "ok": ok, "status": status, "reason": reason})
}

func (s *Server) handleSyncAll(_ context.Context, _ map[string]any) (string, error) {
	result := s.engine.SyncAll(nil)
	ops.Audit(s.db, "sync.run", nil, "", "MCP 触发全量同步")
	return textResult(result)
}

func (s *Server) handleBatchTag(_ context.Context, args map[string]any) (string, error) {
	ids := intSliceArg(args, "ids")
	tags := strSliceArg(args, "tags")
	if len(ids) == 0 || len(tags) == 0 {
		return "", fmt.Errorf("ids 与 tags 都不能为空")
	}
	accounts, err := s.cfg.Accounts()
	if err != nil {
		return "", err
	}
	want := map[int]bool{}
	for _, id := range ids {
		want[id] = true
	}
	changed := 0
	for i := range accounts {
		if !want[accounts[i].ID] {
			continue
		}
		merged := append([]string{}, accounts[i].Tags...)
		for _, t := range tags {
			if !contains(merged, t) {
				merged = append(merged, t)
			}
		}
		accounts[i].Tags = merged
		changed++
	}
	if changed > 0 {
		if err := s.cfg.Save(accounts); err != nil {
			return "", err
		}
	}
	return textResult(map[string]any{"ok": true, "updated": changed})
}

func (s *Server) handleGetAudit(_ context.Context, args map[string]any) (string, error) {
	limit := int(numArg(args, "limit"))
	if limit <= 0 {
		limit = 20
	}
	items, err := ops.ListAudit(s.db, limit, "")
	if err != nil {
		return "", err
	}
	return textResult(map[string]any{"items": items, "total": len(items)})
}

func (s *Server) accountRef(acc config.Account) github.AccountRef {
	return github.AccountRef{ID: acc.ID, Username: acc.Username, Token: acc.Token}
}

func (s *Server) handleStarRepo(_ context.Context, args map[string]any) (string, error) {
	acc, err := s.findAccount(int(numArg(args, "accountId")))
	if err != nil {
		return "", err
	}
	owner := strArg(args, "owner")
	repo := strArg(args, "repo")
	if owner == "" || repo == "" {
		return "", fmt.Errorf("owner 与 repo 不能为空")
	}
	result, err := github.StarRepo(s.accountRef(acc), owner, repo)
	if err != nil {
		return "", err
	}
	ops.Audit(s.db, "github.star", &acc.ID, acc.Username, "MCP star "+owner+"/"+repo)
	return textResult(result)
}

func (s *Server) handleFollowUser(_ context.Context, args map[string]any) (string, error) {
	acc, err := s.findAccount(int(numArg(args, "accountId")))
	if err != nil {
		return "", err
	}
	login := strArg(args, "username")
	if login == "" {
		return "", fmt.Errorf("username 不能为空")
	}
	result, err := github.FollowUser(s.accountRef(acc), login)
	if err != nil {
		return "", err
	}
	ops.Audit(s.db, "github.follow", &acc.ID, acc.Username, "MCP 关注 "+login)
	return textResult(result)
}

func (s *Server) handleCreatePR(ctx context.Context, args map[string]any) (string, error) {
	acc, err := s.findAccount(int(numArg(args, "accountId")))
	if err != nil {
		return "", err
	}
	repo := strArg(args, "repo")
	title := strArg(args, "title")
	if repo == "" || title == "" {
		return "", fmt.Errorf("repo 与 title 不能为空")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	result, err := github.CreatePR(ctx, s.accountRef(acc), repo, title, github.CreatePROptions{
		Base:     strArg(args, "base"),
		Head:     strArg(args, "head"),
		Body:     strArg(args, "body"),
		Draft:    boolArg(args, "draft"),
		Fork:     boolArg(args, "fork"),
		Branch:   strArg(args, "branch"),
		FilePath: strArg(args, "filePath"),
		Content:  strArg(args, "fileContent"),
	})
	if err != nil {
		return "", err
	}
	ops.Audit(s.db, "github.create_pr", &acc.ID, acc.Username, "MCP 创建 PR："+repo)
	return textResult(result)
}

func boolArg(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func numArg(args map[string]any, key string) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	default:
		return 0
	}
}

func intSliceArg(args map[string]any, key string) []int {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]int, 0, len(raw))
	for _, v := range raw {
		switch n := v.(type) {
		case float64:
			out = append(out, int(n))
		case string:
			if i, err := strconv.Atoi(n); err == nil {
				out = append(out, i)
			}
		}
	}
	return out
}

func strSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
