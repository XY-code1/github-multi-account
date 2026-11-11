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
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github-multi-account/internal/config"
	"github-multi-account/internal/server"
	"github-multi-account/internal/service"
	"github-multi-account/internal/store"
)

//go:embed all:assets
var assetsFS embed.FS

var candidatePorts = []int{8000, 8080, 8888, 5000, 3000, 9000}

func main() {
	base := baseDir()
	configDir := filepath.Join(base, "config")
	dataDir := filepath.Join(base, "data")

	if err := run(configDir, dataDir); err != nil {
		showError(err)
		os.Exit(1)
	}
	// 服务在后台 goroutine 中运行，主协程阻塞保持进程存活
	select {}
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

func run(configDir, dataDir string) error {
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

	port := findPort()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", addr, err)
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

func findPort() int {
	for _, p := range candidatePorts {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			_ = ln.Close()
			return p
		}
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
