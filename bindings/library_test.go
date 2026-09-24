package bindings

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// resetForTest 清空加载状态并重新安装 mock，仅供测试使用。
func resetForTest() {
	libState.Lock()
	if libState.loaded && libState.handle != 0 {
		_ = closeLibrary(libState.handle)
	}
	libState.handle = 0
	libState.loaded = false
	libState.loadErr = nil
	libState.Unlock()
	setMockImplementations()
}

// puregoSupportsBindings 报告当前平台 purego v0.7.0 是否支持本绑定中的结构体参数。
// purego 目前只在 darwin/amd64 与 darwin/arm64 上支持结构体参数。
func puregoSupportsBindings() bool {
	return runtime.GOOS == "darwin"
}

func TestDefaultLibName(t *testing.T) {
	want := map[string]string{
		"darwin":  "libstable-diffusion.dylib",
		"linux":   "libstable-diffusion.so",
		"windows": "stable-diffusion.dll",
	}
	name, ok := libNames[runtime.GOOS]
	if !ok {
		t.Fatalf("no default library name for platform %s", runtime.GOOS)
	}
	if name != want[runtime.GOOS] {
		t.Fatalf("platform %s: got %q, want %q", runtime.GOOS, name, want[runtime.GOOS])
	}
}

func TestDefaultSearchPathsContainBinDir(t *testing.T) {
	name := libNames[runtime.GOOS]
	paths := defaultSearchPaths(name)
	if len(paths) == 0 {
		t.Fatal("defaultSearchPaths returned no candidates")
	}
	found := false
	for _, p := range paths {
		if p == name {
			found = true
		}
		if filepath.Separator != '/' && !filepath.IsAbs(p) && strings.Contains(p, "/") {
			t.Errorf("windows path contains slash: %s", p)
		}
	}
	if !found {
		t.Errorf("bare library name %q missing from candidates %v", name, paths)
	}
}

func TestLoadEnvPathMissingFile(t *testing.T) {
	resetForTest()
	missing := filepath.Join(t.TempDir(), "does-not-exist-"+libNames[runtime.GOOS])
	t.Setenv(EnvLibPath, missing)

	err := Load()
	if err == nil {
		t.Fatal("expected error when SD_LIB_PATH points to a missing file, got nil")
	}
	if IsLoaded() {
		t.Fatal("library should not be marked loaded after failure")
	}

	var loadErr *LibraryLoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected *LibraryLoadError, got %T: %v", err, err)
	}
	if loadErr.Platform != runtime.GOOS {
		t.Errorf("Platform = %q, want %q", loadErr.Platform, runtime.GOOS)
	}
	if loadErr.Arch != runtime.GOARCH {
		t.Errorf("Arch = %q, want %q", loadErr.Arch, runtime.GOARCH)
	}
	if loadErr.EnvPath != missing {
		t.Errorf("EnvPath = %q, want %q", loadErr.EnvPath, missing)
	}
	if len(loadErr.Candidates) != 1 || loadErr.Candidates[0] != missing {
		t.Errorf("Candidates = %v, want only [%s]", loadErr.Candidates, missing)
	}
	msg := err.Error()
	for _, want := range []string{runtime.GOOS, runtime.GOARCH, missing} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q does not contain %q", msg, want)
		}
	}
	if LoadError() == nil {
		t.Error("LoadError() should retain the last failure")
	}
}

func TestLoadFailureFallsBackToMock(t *testing.T) {
	resetForTest()
	t.Setenv(EnvLibPath, filepath.Join(t.TempDir(), libNames[runtime.GOOS]))

	if err := LoadOrMock(); err == nil {
		t.Fatal("LoadOrMock should return the load error")
	}
	if IsLoaded() {
		t.Fatal("mock mode must not report the library as loaded")
	}
	// mock 实现保证基础 API 仍可调用、不 panic。
	if got := GetVersion(); got == "" {
		t.Error("mock GetVersion returned empty string")
	}
	if got := GetNumPhysicalCores(); got <= 0 {
		t.Errorf("mock GetNumPhysicalCores = %d, want > 0", got)
	}
}

// buildStubLib 用 gcc 编译一个包含全部导出桩符号的共享库，返回其路径。
func buildStubLib(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not available")
	}
	switch runtime.GOOS {
	case "linux":
	case "darwin":
	default:
		t.Skipf("stub library build not configured for %s", runtime.GOOS)
	}

	dir := t.TempDir()
	src := filepath.Join(dir, "stub.c")
	var b strings.Builder
	b.WriteString("#include <stdint.h>\n")
	for _, bnd := range libFuncBindings() {
		b.WriteString("void " + bnd.name + "(void) {}\n")
	}
	if err := os.WriteFile(src, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	var libPath string
	var args []string
	if runtime.GOOS == "darwin" {
		libPath = filepath.Join(dir, "libstable-diffusion.dylib")
		args = []string{"-dynamiclib", "-fPIC", "-o", libPath, src}
	} else {
		libPath = filepath.Join(dir, "libstable-diffusion.so")
		args = []string{"-shared", "-fPIC", "-o", libPath, src}
	}
	cmd := exec.Command("gcc", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("failed to build stub library: %v\n%s", err, out)
	}
	return libPath
}

func TestLoadExistingLibrary(t *testing.T) {
	libPath := buildStubLib(t)
	resetForTest()
	t.Cleanup(resetForTest)
	t.Setenv(EnvLibPath, libPath)

	err := Load()
	if !puregoSupportsBindings() {
		// 非 darwin 平台 purego 不支持本绑定所需的结构体参数：
		// 库本身应能打开、所有符号也应能解析，但注册函数指针时返回带原因的错误而不是 panic。
		if err == nil {
			t.Fatal("expected symbol registration error on platforms without struct support, got nil")
		}
		if IsLoaded() {
			t.Fatal("library must not be marked loaded after a registration failure")
		}
		if !strings.Contains(err.Error(), libPath) {
			t.Errorf("registration error %q should mention path %s", err, libPath)
		}
		var loadErr *LibraryLoadError
		// 注册阶段错误不属于 *LibraryLoadError，但不得 panic、不得留下野指针。
		if errors.As(err, &loadErr) {
			t.Errorf("registration failure should not be reported as a load error")
		}
		return
	}

	if err != nil {
		t.Fatalf("Load failed for existing stub library: %v", err)
	}
	if !IsLoaded() {
		t.Fatal("IsLoaded = false after successful Load")
	}
	if err := LoadError(); err != nil {
		t.Errorf("LoadError = %v after success, want nil", err)
	}
	// 重复加载应当是幂等的。
	if err := Load(); err != nil {
		t.Errorf("second Load returned error: %v", err)
	}
}

func TestRetryAfterFixingPath(t *testing.T) {
	libPath := buildStubLib(t)
	resetForTest()
	t.Cleanup(resetForTest)

	missing := filepath.Join(t.TempDir(), "missing-"+libNames[runtime.GOOS])
	t.Setenv(EnvLibPath, missing)
	if err := Load(); err == nil {
		t.Fatal("expected first Load to fail")
	}

	// 修正环境变量后应允许重试。
	t.Setenv(EnvLibPath, libPath)
	err := Load()
	if !puregoSupportsBindings() {
		// 同上：库能打开但当前平台不支持结构体参数注册。
		if err == nil {
			t.Fatal("expected registration error on platforms without struct support")
		}
		return
	}
	if err != nil {
		t.Fatalf("retry Load failed after fixing path: %v", err)
	}
	if !IsLoaded() {
		t.Fatal("IsLoaded = false after successful retry")
	}
}
