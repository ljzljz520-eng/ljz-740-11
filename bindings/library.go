package bindings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/ebitengine/purego"
)

// EnvLibPath 是用于指定动态库路径的环境变量名。
// 设置后将只加载该路径，不再搜索默认位置。
const EnvLibPath = "SD_LIB_PATH"

// libNames 是各平台对应的默认动态库文件名。
var libNames = map[string]string{
	"darwin":  "libstable-diffusion.dylib", // macOS
	"linux":   "libstable-diffusion.so",
	"windows": "stable-diffusion.dll",
}

// LibraryLoadError 描述一次动态库加载失败，包含平台信息和尝试过的路径。
type LibraryLoadError struct {
	// Platform 是 runtime.GOOS。
	Platform string
	// Arch 是 runtime.GOARCH。
	Arch string
	// EnvPath 是通过环境变量指定的路径（未设置时为空）。
	EnvPath string
	// Candidates 是所有尝试过的候选路径。
	Candidates []string
	// Errors 与 Candidates 一一对应，记录每个路径失败的原因。
	Errors []error
}

// Error 实现 error 接口，不使用 panic。
func (e *LibraryLoadError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "failed to load stable-diffusion dynamic library on %s/%s", e.Platform, e.Arch)
	if e.EnvPath != "" {
		fmt.Fprintf(&b, " (%s=%s)", EnvLibPath, e.EnvPath)
	}
	for i, p := range e.Candidates {
		fmt.Fprintf(&b, "\n  - %s: %v", p, e.Errors[i])
	}
	return b.String()
}

// Unwrap 返回底层错误，支持 errors.Is / errors.As。
func (e *LibraryLoadError) Unwrap() error {
	return errors.Join(e.Errors...)
}

// libState 管理动态库句柄和已解析的函数指针，全程加锁访问。
var libState struct {
	sync.Mutex
	handle  uintptr
	loaded  bool
	loadErr error
}

// defaultSearchPaths 返回当前平台下默认动态库文件名的候选位置。
func defaultSearchPaths(name string) []string {
	// 裸库名交给系统加载器按其内置搜索规则（LD_LIBRARY_PATH、
	// DYLD_LIBRARY_PATH、PATH 等）查找，其余为显式的相对/绝对路径。
	paths := []string{name}

	// 常见构建输出目录。
	paths = append(paths,
		filepath.Join("stable-diffusion.cpp", "build", "bin", name),
	)
	if runtime.GOOS == "windows" {
		// Windows 下多配置生成器（Visual Studio）会把 DLL 放到 Release/Debug 子目录。
		paths = append(paths,
			filepath.Join("stable-diffusion.cpp", "build", "bin", "Release", name),
			filepath.Join("stable-diffusion.cpp", "build", "bin", "Debug", name),
		)
	} else {
		// Unix 系统标准安装位置。
		paths = append(paths,
			filepath.Join("/usr", "local", "lib", name),
			filepath.Join("/usr", "lib", name),
		)
		if runtime.GOARCH == "arm64" || runtime.GOARCH == "arm" {
			paths = append(paths, filepath.Join("/opt", "homebrew", "lib", name))
		}
	}
	return paths
}

// candidatePaths 返回本次加载要尝试的库路径，以及是否来自环境变量。
func candidatePaths() (paths []string, envPath string, unsupported bool) {
	if p, ok := os.LookupEnv(EnvLibPath); ok {
		// 用户显式指定路径时只尝试这一个路径。
		return []string{p}, p, false
	}
	name, ok := libNames[runtime.GOOS]
	if !ok {
		return nil, "", true
	}
	return defaultSearchPaths(name), "", false
}

// Load 加载平台对应的动态库并绑定所有导出函数。
//
// 解析顺序：
//  1. 环境变量 SD_LIB_PATH 指定的路径（仅尝试该路径）；
//  2. 当前平台的默认库文件名，并在工作目录、构建输出目录及系统库目录中搜索。
//
// 库一旦加载成功，后续调用直接返回 nil；加载失败后可以修正路径/环境变量重试。
// 任何失败都会返回包含平台 (GOOS/GOARCH) 与候选路径信息的 *LibraryLoadError，不会 panic。
func Load() error {
	libState.Lock()
	defer libState.Unlock()

	if libState.loaded && libState.handle != 0 {
		return nil
	}

	paths, envPath, unsupported := candidatePaths()
	if unsupported {
		libState.loadErr = &LibraryLoadError{
			Platform:   runtime.GOOS,
			Arch:       runtime.GOARCH,
			Candidates: paths,
			Errors:     []error{fmt.Errorf("unsupported platform: no default library name for %q", runtime.GOOS)},
		}
		return libState.loadErr
	}

	loadErr := &LibraryLoadError{
		Platform: runtime.GOOS,
		Arch:     runtime.GOARCH,
		EnvPath:  envPath,
	}

	var handle uintptr
	var err error
	loadedPath := ""
	for _, p := range paths {
		if envPath != "" {
			// 用户显式指定路径时，先给出“文件不存在”这类更清晰的错误。
			if _, statErr := os.Stat(p); statErr != nil {
				loadErr.Candidates = append(loadErr.Candidates, p)
				loadErr.Errors = append(loadErr.Errors, statErr)
				continue
			}
		}
		handle, err = openLibrary(p)
		if err == nil && handle != 0 {
			loadedPath = p
			loadErr = nil
			break
		}
		loadErr.Candidates = append(loadErr.Candidates, p)
		loadErr.Errors = append(loadErr.Errors, err)
	}

	if loadErr != nil {
		libState.loadErr = loadErr
		return loadErr
	}

	// 绑定函数指针。RegisterLibFunc 在找不到符号时会 panic，
	// 这里先用 Dlsym 预检并 recover 兜底，保证库加载流程绝不 panic。
	if err := bindLibFuncs(handle); err != nil {
		_ = closeLibrary(handle)
		libState.loadErr = fmt.Errorf("failed to resolve symbols from %s: %w", loadedPath, err)
		return libState.loadErr
	}

	libState.handle = handle
	libState.loaded = true
	libState.loadErr = nil
	return nil
}

// LoadOrMock 尝试加载动态库；失败时安装 mock 实现并返回加载错误（不为 nil）。
// 适用于希望在缺少动态库的环境（如 CI）中保持程序可用的调用方。
func LoadOrMock() error {
	if err := Load(); err != nil {
		setMockImplementations()
		return err
	}
	return nil
}

// IsLoaded 返回动态库是否已成功加载。
func IsLoaded() bool {
	libState.Lock()
	defer libState.Unlock()
	return libState.loaded && libState.handle != 0
}

// LoadError 返回最近一次加载失败的错误；从未尝试或已成功时返回 nil。
func LoadError() error {
	libState.Lock()
	defer libState.Unlock()
	if libState.loaded {
		return nil
	}
	return libState.loadErr
}

// libFuncBinding 描述一个导出符号与对应的函数指针变量。
type libFuncBinding struct {
	name string
	fptr interface{}
}

// libFuncBindings 是需要从动态库解析的全部符号，顺序与头文件导出保持一致。
func libFuncBindings() []libFuncBinding {
	return []libFuncBinding{
		{"sd_set_log_callback", &sdSetLogCallback},
		{"sd_set_progress_callback", &sdSetProgressCallback},
		{"sd_set_preview_callback", &sdSetPreviewCallback},
		{"sd_get_num_physical_cores", &sdGetNumPhysicalCores},
		{"sd_get_system_info", &sdGetSystemInfo},
		{"sd_type_name", &sdTypeName},
		{"str_to_sd_type", &strToSdType},
		{"sd_rng_type_name", &sdRngTypeName},
		{"str_to_rng_type", &strToRngType},
		{"sd_sample_method_name", &sdSampleMethodName},
		{"str_to_sample_method", &strToSampleMethod},
		{"sd_scheduler_name", &sdSchedulerName},
		{"str_to_scheduler", &strToScheduler},
		{"sd_prediction_name", &sdPredictionName},
		{"str_to_prediction", &strToPrediction},
		{"sd_preview_name", &sdPreviewName},
		{"str_to_preview", &strToPreview},
		{"sd_lora_apply_mode_name", &sdLoraApplyModeName},
		{"str_to_lora_apply_mode", &strToLoraApplyMode},
		{"sd_cache_params_init", &sdCacheParamsInit},
		{"sd_ctx_params_init", &sdCtxParamsInit},
		{"sd_ctx_params_to_str", &sdCtxParamsToStr},
		{"new_sd_ctx", &newSdCtx},
		{"free_sd_ctx", &freeSdCtx},
		{"sd_sample_params_init", &sdSampleParamsInit},
		{"sd_sample_params_to_str", &sdSampleParamsToStr},
		{"sd_get_default_sample_method", &sdGetDefaultSampleMethod},
		{"sd_get_default_scheduler", &sdGetDefaultScheduler},
		{"sd_img_gen_params_init", &sdImgGenParamsInit},
		{"sd_img_gen_params_to_str", &sdImgGenParamsToStr},
		{"generate_image", &generateImage},
		{"sd_vid_gen_params_init", &sdVidGenParamsInit},
		{"generate_video", &generateVideo},
		{"new_upscaler_ctx", &newUpscalerCtx},
		{"free_upscaler_ctx", &freeUpscalerCtx},
		{"upscale", &upscale},
		{"get_upscale_factor", &getUpscaleFactor},
		{"convert", &convert},
		{"preprocess_canny", &preprocessCanny},
		{"sd_commit", &sdCommit},
		{"sd_version", &sdVersion},
	}
}

// bindLibFuncs 预检所有符号后再逐个注册，缺符号时返回错误而不是 panic。
func bindLibFuncs(handle uintptr) (err error) {
	bindings := libFuncBindings()

	// 预检：任何一个符号缺失都不修改函数指针。
	for _, b := range bindings {
		if _, e := lookupSymbol(handle, b.name); e != nil {
			return fmt.Errorf("symbol %q not found: %w", b.name, e)
		}
	}

	// 保存注册前的函数指针（独立副本，避免地址化 Value 随后续赋值失效），
	// 注册中途失败时回滚，避免留下指向已卸载库的野指针。
	saved := make([]reflect.Value, len(bindings))
	for i, b := range bindings {
		cur := reflect.ValueOf(b.fptr).Elem()
		snapshot := reflect.New(cur.Type()).Elem()
		snapshot.Set(cur)
		saved[i] = snapshot
	}
	rollback := func() {
		for i, b := range bindings {
			reflect.ValueOf(b.fptr).Elem().Set(saved[i])
		}
	}

	// 预检通过后注册，理论上不会再 panic；recover 仅作兜底。
	defer func() {
		if r := recover(); r != nil {
			rollback()
			err = fmt.Errorf("panic while registering library functions: %v", r)
		}
	}()
	for _, b := range bindings {
		purego.RegisterLibFunc(b.fptr, handle, b.name)
	}
	return nil
}

// init 在包初始化时尝试加载动态库；加载失败则回退到 mock 实现，
// 错误可通过 LoadError() 获取，也可以在修正环境后调用 Load() 重试。
func init() {
	if err := Load(); err != nil {
		setMockImplementations()
	}
}
