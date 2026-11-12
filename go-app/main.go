// GitHub 多账号状态仪表盘 —— 单文件桌面版。
//
// 构建（Windows，无控制台窗口）：
//
//	GOOS=windows GOARCH=amd64 go build -ldflags "-H windowsgui -s -w" -o "GitHub仪表盘.exe" .
//
// 构建（本机测试）：
//
//	go build -o github-dashboard .
package main

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github-multi-account/internal/config"
	"github-multi-account/internal/notify"
	"github-multi-account/internal/ops"
	"github-multi-account/internal/server"
	"github-multi-account/internal/service"
	"github-multi-account/internal/store"
)

//go:embed all:assets
var assetsFS embed.FS

var candidatePorts = []int{8000, 8080, 8888, 5000, 3000, 9000, 8001, 8081, 9090}

func main() {
	base := baseDir()

	err := run(base)
	switch {
	case err == nil:
		// 服务在后台 goroutine 中运行，主协程阻塞保持进程存活
		select {}
	case errors.Is(err, errAlreadyRunning):
		// 已有实例在运行，浏览器已打开，正常退出
	default:
		showError(err)
		os.Exit(1)
	}
}

// errAlreadyRunning 表示检测到已有实例在运行。
var errAlreadyRunning = errors.New("already running")

// isAppRunning 探测目标端口上是否已经是本应用。
func isAppRunning(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/api/accounts", port))
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

func baseDir() string {
	if v := os.Getenv("GAD_BASE_DIR"); v != "" {
		return v
	}
	if exe, err := os.Executable(); err == nil {
		if p, err := filepath.EvalSymlinks(exe); err == nil {
			return filepath.Dir(p)
		}
		return filepath.Dir(exe)
	}
	wd, _ := os.Getwd()
	return wd
}

func run(base string) error {
	configDir := filepath.Join(base, "config")
	dataDir := filepath.Join(base, "data")

	cfg, err := config.Open(configDir)
	if err != nil {
		return err
	}
	st, err := store.Open(dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	if n, err := st.Prune(1000, 90); err == nil && n > 0 {
		log.Printf("已清理历史快照 %d 条", n)
	}

	svc := &service.Service{Store: st}
	assetsSub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		return fmt.Errorf("加载前端资源失败: %w", err)
	}
	srv := server.New(cfg, svc, st, assetsSub)
	srv.SetBaseDir(base)

	// 运维中心：建表、重建账号元数据镜像、挂接后台同步引擎
	if err := ops.Init(st.DB()); err != nil {
		return fmt.Errorf("初始化运维中心数据库失败: %w", err)
	}
	accounts, err := cfg.Accounts()
	if err != nil {
		return err
	}
	engine := ops.NewEngine(st.DB(), cfg, svc, configDir)
	srv.SetOpsDB(st.DB())
	srv.SetEngine(engine)
	srv.SetConfigDir(configDir)
	srv.RebuildMeta()

	poller := notify.NewPoller(base, 5*time.Minute, cfg, svc)
	srv.SetPoller(poller)
	if len(notify.LoadChannels(base)) > 0 {
		poller.Start()
		log.Printf("外部通知轮询已启动")
	}
	defer poller.Stop()

	if len(accounts) > 0 {
		engine.Start()
		c := engine.Config()
		log.Printf("后台同步引擎已启动（间隔 %ds，并发 %d）", c.IntervalSeconds, c.Concurrency)
	}
	defer engine.Stop()

	// 已有实例在运行时，只打开浏览器，避免启动第二个服务
	if p := readSavedPort(dataDir); p > 0 && isAppRunning(p) {
		log.Printf("检测到已有实例运行在 :%d，仅打开浏览器", p)
		openBrowser(fmt.Sprintf("http://localhost:%d", p))
		return errAlreadyRunning
	}

	port := choosePort(dataDir)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w。提示：Windows 上 Hyper-V / WSL / Docker 可能保留了部分端口段，可用 netsh interface ipv4 show excludedportrange protocol=tcp 查看", addr, err)
	}
	httpSrv := &http.Server{Handler: srv.Handler()}
	log.Printf("GitHub 多账号仪表盘已启动: http://%s", addr)

	errCh := make(chan error, 1)
	go func() {
		if e := httpSrv.Serve(ln); e != nil && e != http.ErrServerClosed {
			errCh <- e
		}
	}()

	if !waitForReady(port, 15*time.Second) {
		return fmt.Errorf("服务未能在 15 秒内就绪")
	}
	openBrowser(fmt.Sprintf("http://localhost:%d", port))

	select {
	case e := <-errCh:
		return e
	case <-time.After(time.Hour * 24 * 365):
		return nil
	}
}

func portAvailable(p int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// osAssignedPort 让操作系统分配一个空闲端口（系统不会分配被保留的端口段）。
func osAssignedPort() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer func() { _ = ln.Close() }()
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		return addr.Port
	}
	return 0
}

func portFile(dataDir string) string { return filepath.Join(dataDir, "server.port") }

func readSavedPort(dataDir string) int {
	b, err := os.ReadFile(portFile(dataDir))
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || p < 1 || p > 65535 {
		return 0
	}
	return p
}

func savePort(dataDir string, p int) {
	_ = os.MkdirAll(dataDir, 0o755)
	_ = os.WriteFile(portFile(dataDir), []byte(strconv.Itoa(p)), 0o644)
}

// choosePort 选一个可用端口：优先沿用上次成功的端口，其次用固定候选端口，
// 最后让系统分配 —— 避免 Windows 保留端口段导致启动失败。
func choosePort(dataDir string) int {
	if p := readSavedPort(dataDir); p > 0 && portAvailable(p) {
		return p
	}
	for _, p := range candidatePorts {
		if portAvailable(p) {
			savePort(dataDir, p)
			return p
		}
	}
	if p := osAssignedPort(); p > 0 {
		savePort(dataDir, p)
		return p
	}
	return 8000
}

func waitForReady(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

var _ fs.FS = assetsFS
