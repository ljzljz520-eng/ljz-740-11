// 动态库加载：识别 macOS / Linux / Windows 对应的库文件名，
// 支持通过环境变量 SD_LIB_PATH 指定路径。
//
// 加载失败时返回 *LoadError（携带平台、架构、尝试过的路径等信息），
// 全程不会 panic。

package bindings

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// EnvLibPath 是用于指定动态库路径的环境变量名。
const EnvLibPath = "SD_LIB_PATH"

// 各平台对应的默认动态库文件名（与 stable-diffusion.cpp 的 CMakeLists.txt 一致）。
var platformLibNames = map[string]string{
	"darwin":  "libstable-diffusion.dylib",
	"linux":   "libstable-diffusion.so",
	"windows": "stable-diffusion.dll",
}

// LoadError 表示动态库加载失败，携带定位问题所需的上下文。
type LoadError struct {
	// Platform 是 runtime.GOOS，例如 darwin / linux / windows。
	Platform string
	// Arch 是 runtime.GOARCH，例如 amd64 / arm64。
	Arch string
	// EnvPath 是环境变量 SD_LIB_PATH 的值（为空表示未设置）。
	EnvPath string
	// Candidate 是最终实际尝试加载的路径（环境变量路径，或搜索到的路径，或裸库名）。
	Candidate string
	// Searched 是在默认搜索过程中检查过的全部候选路径。
	Searched []string
	// Err 是底层加载器返回的错误。
	Err error
}

func (e *LoadError) Error() string {
	switch {
	case e.EnvPath != "":
		return fmt.Sprintf("stablediffusion: failed to load shared library on %s/%s from %s (env %s): %v",
			e.Platform, e.Arch, e.Candidate, EnvLibPath, e.Err)
	case e.Candidate != "":
		return fmt.Sprintf("stablediffusion: failed to load shared library %q on %s/%s (searched: %v): %v",
			e.Candidate, e.Platform, e.Arch, e.Searched, e.Err)
	default:
		return fmt.Sprintf("stablediffusion: failed to load shared library on %s/%s (searched: %v): %v",
			e.Platform, e.Arch, e.Searched, e.Err)
	}
}

// Unwrap 支持 errors.Is / errors.As 取出底层错误。
func (e *LoadError) Unwrap() error { return e.Err }

var (
	loadMu  sync.Mutex
	lib     uintptr
	libPath string
)

// LoadLibrary 加载 stable-diffusion 动态库并注册全部 C 函数指针。
//
// 解析顺序：
//  1. 环境变量 SD_LIB_PATH 指定的路径（可指向具体文件，直接加载）；
//  2. 当前平台默认库名，依次在常见目录中搜索；
//  3. 都找不到时退回裸库名，交给系统加载器（LD_LIBRARY_PATH / DYLD_* / PATH）。
//
// 该函数可重复调用：成功后再次调用直接返回 nil；加载失败不会修改已加载状态。
func LoadLibrary() error {
	loadMu.Lock()
	defer loadMu.Unlock()

	if lib != 0 {
		return nil
	}

	platform := runtime.GOOS
	arch := runtime.GOARCH
	envPath := os.Getenv(EnvLibPath)

	var path string
	var searched []string
	if envPath != "" {
		// 用户显式指定路径，原样尝试，不做搜索。
		path = envPath
	} else {
		name, err := DefaultLibName()
		if err != nil {
			return &LoadError{
				Platform: platform,
				Arch:     arch,
				EnvPath:  envPath,
				Searched: searched,
				Err:      err,
			}
		}
		path, searched = resolveLibPath(name)
	}

	handle, err := openLibrary(path)
	if err != nil {
		return &LoadError{
			Platform:  platform,
			Arch:      arch,
			EnvPath:   envPath,
			Candidate: path,
			Searched:  searched,
			Err:       err,
		}
	}

	// RegisterLibFunc 在缺少符号时会 panic，这里统一转换为错误返回，
	// 保证加载流程绝不 panic。
	if err := registerSafely(handle); err != nil {
		// 符号不匹配说明不是期望的库，关闭句柄避免泄漏。
		closeLibrary(handle)
		return &LoadError{
			Platform:  platform,
			Arch:      arch,
			EnvPath:   envPath,
			Candidate: path,
			Searched:  searched,
			Err:       err,
		}
	}

	lib = handle
	libPath = path
	return nil
}

// registerSafely 执行符号注册并把 panic 转为普通错误。
func registerSafely(handle uintptr) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("symbol resolution failed: %v", r)
		}
	}()
	registerFuncs(handle)
	return nil
}

// DefaultLibName 返回当前平台对应的默认动态库文件名。
// 在不支持的平台上返回错误。
func DefaultLibName() (string, error) {
	if name, ok := platformLibNames[runtime.GOOS]; ok {
		return name, nil
	}
	return "", fmt.Errorf("unsupported platform: %s/%s", runtime.GOOS, runtime.GOARCH)
}

// resolveLibPath 在默认搜索路径中查找库文件：
// 找到第一个存在的文件则返回其路径；否则返回裸库名（交由系统加载器）
// 以及全部已检查的候选路径。
func resolveLibPath(name string) (string, []string) {
	candidates := searchCandidates(name)
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, candidates
		}
	}
	return name, candidates
}

// searchCandidates 返回当前平台的默认候选路径（已去重）。
func searchCandidates(name string) []string {
	dirs := defaultSearchDirs()
	seen := make(map[string]struct{}, len(dirs)+1)
	candidates := make([]string, 0, len(dirs)+1)

	add := func(p string) {
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		candidates = append(candidates, p)
	}

	// 裸库名优先，交给系统加载器的默认搜索逻辑。
	add(name)
	for _, dir := range dirs {
		add(filepath.Join(dir, name))
	}
	return candidates
}

// IsLoaded 返回动态库是否已成功加载。
func IsLoaded() bool {
	loadMu.Lock()
	defer loadMu.Unlock()
	return lib != 0
}

// LibraryPath 返回实际加载成功的库路径；未加载时返回空字符串。
func LibraryPath() string {
	loadMu.Lock()
	defer loadMu.Unlock()
	return libPath
}
