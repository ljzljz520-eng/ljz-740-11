//go:build windows

package bindings

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// openLibrary 在 Windows 平台通过 LoadLibrary 加载 DLL。
func openLibrary(path string) (uintptr, error) {
	handle, err := windows.LoadLibrary(path)
	if err != nil {
		return 0, err
	}
	return uintptr(handle), nil
}

// lookupSymbol 通过 GetProcAddress 查找导出符号。
func lookupSymbol(handle uintptr, name string) (uintptr, error) {
	proc, err := windows.GetProcAddress(windows.Handle(handle), name)
	if err != nil {
		return 0, fmt.Errorf("GetProcAddress(%s): %w", name, err)
	}
	return proc, nil
}

// closeLibrary 通过 FreeLibrary 释放句柄。
func closeLibrary(handle uintptr) error {
	return windows.FreeLibrary(windows.Handle(handle))
}
