//go:build windows

package bindings

import (
	"golang.org/x/sys/windows"
)

// defaultSearchDirs 返回 Windows 上的默认搜索目录。
// 裸 DLL 名也会作为候选（见 searchCandidates），LoadLibrary 会在
// 应用目录、系统目录以及 PATH 中自动搜索。
func defaultSearchDirs() []string {
	dirs := []string{
		".",
		`.\stable-diffusion.cpp\build\bin`,
		`.\stable-diffusion.cpp\build\bin\Release`,
	}
	// 系统目录（通常为 C:\Windows\System32），获取不到则跳过。
	if sys, err := windows.GetSystemDirectory(); err == nil && sys != "" {
		dirs = append(dirs, sys)
	}
	return dirs
}

// openLibrary 通过 LoadLibrary 加载 DLL。
func openLibrary(path string) (uintptr, error) {
	handle, err := windows.LoadLibrary(path)
	if err != nil {
		return 0, err
	}
	return uintptr(handle), nil
}

// closeLibrary 释放 DLL 句柄（仅在符号注册失败的回退路径使用）。
func closeLibrary(handle uintptr) {
	_ = windows.FreeLibrary(windows.Handle(handle))
}
