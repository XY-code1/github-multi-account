//go:build !windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func showError(err error) {
	fmt.Fprintf(os.Stderr, "启动失败: %v\n", err)
	dir := filepath.Join(baseDir(), "data")
	_ = os.MkdirAll(dir, 0o755)
	if f, e := os.OpenFile(filepath.Join(dir, "error.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); e == nil {
		defer func() { _ = f.Close() }()
		fmt.Fprintf(f, "%s %v\n", time.Now().Format(time.RFC3339), err)
	}
}
