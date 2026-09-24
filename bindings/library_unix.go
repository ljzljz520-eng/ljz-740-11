//go:build darwin || linux

package bindings

import "github.com/ebitengine/purego"

// openLibrary 在 Unix 平台通过 dlopen 打开动态库（.dylib / .so）。
func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}

// lookupSymbol 通过 dlsym 查找导出符号。
func lookupSymbol(handle uintptr, name string) (uintptr, error) {
	return purego.Dlsym(handle, name)
}

// closeLibrary 通过 dlclose 释放句柄。
func closeLibrary(handle uintptr) error {
	return purego.Dlclose(handle)
}
