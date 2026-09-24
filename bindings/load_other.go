//go:build !darwin && !linux && !windows

package bindings

import "fmt"

// defaultSearchDirs 在不支持的平台上没有可搜索的目录。
func defaultSearchDirs() []string {
	return nil
}

// openLibrary 在不支持的平台上直接返回错误，不 panic。
func openLibrary(path string) (uintptr, error) {
	return 0, fmt.Errorf("dynamic library loading is not supported on this platform")
}

// closeLibrary 在不支持的平台上为空操作。
func closeLibrary(handle uintptr) {}
