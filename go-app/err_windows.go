//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

func showError(err error) {
	msg := fmt.Sprintf("启动失败：\n%v", err)

	// 写入错误日志（data/error.log）
	if dir := filepath.Join(baseDir(), "data"); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		if f, e := os.OpenFile(filepath.Join(dir, "error.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); e == nil {
			defer func() { _ = f.Close() }()
			fmt.Fprintf(f, "%s %v\n", time.Now().Format(time.RFC3339), err)
		}
	}

	user32 := syscall.NewLazyDLL("user32.dll")
	messageBox := user32.NewProc("MessageBoxW")
	title, _ := syscall.UTF16PtrFromString("GitHub 仪表盘")
	text, _ := syscall.UTF16PtrFromString(msg)
	_, _, _ = messageBox.Call(0,
		uintptr(unsafe.Pointer(text)),
		uintptr(unsafe.Pointer(title)),
		0x10, // MB_ICONERROR
	)
}
