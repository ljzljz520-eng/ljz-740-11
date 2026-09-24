//go:build darwin || linux

package bindings

import "github.com/ebitengine/purego"

// defaultSearchDirs 返回 macOS / Linux 上的默认搜索目录。
// 裸库名本身也会作为候选（见 searchCandidates），由动态链接器按
// LD_LIBRARY_PATH（Linux）/ DYLD_LIBRARY_PATH（macOS）等机制搜索。
func defaultSearchDirs() []string {
	return []string{
		".",
		"./stable-diffusion.cpp/build/bin",
		"/usr/local/lib",
		"/usr/lib",
	}
}

// openLibrary 通过 dlopen 加载动态库。
func openLibrary(path string) (uintptr, error) {
	return purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}

// closeLibrary 关闭动态库句柄（仅在符号注册失败的回退路径使用）。
func closeLibrary(handle uintptr) {
	_ = purego.Dlclose(handle)
}
