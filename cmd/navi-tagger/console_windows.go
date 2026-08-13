//go:build windows

package main

import "syscall"

// Windows 控制台默认是 GBK，Go 输出的是 UTF-8，不改的话提示信息全是乱码。
func init() {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleOutputCP")
	proc.Call(65001)
}
