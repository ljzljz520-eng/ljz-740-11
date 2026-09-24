package bindings

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultLibName(t *testing.T) {
	want := map[string]string{
		"darwin":  "libstable-diffusion.dylib",
		"linux":   "libstable-diffusion.so",
		"windows": "stable-diffusion.dll",
	}
	name, err := DefaultLibName()
	if err != nil {
		t.Fatalf("DefaultLibName returned error on %s: %v", runtime.GOOS, err)
	}
	if name != want[runtime.GOOS] {
		t.Fatalf("DefaultLibName = %q, want %q", name, want[runtime.GOOS])
	}
}

func TestSearchCandidates(t *testing.T) {
	name, err := DefaultLibName()
	if err != nil {
		t.Fatal(err)
	}
	candidates := searchCandidates(name)
	if len(candidates) == 0 {
		t.Fatal("searchCandidates returned no candidates")
	}
	// 第一个候选必须是裸库名，交由系统加载器处理。
	if candidates[0] != name {
		t.Errorf("first candidate = %q, want bare library name %q", candidates[0], name)
	}

	// 其余候选必须是 "目录/库名" 的形式。
	for _, c := range candidates[1:] {
		if filepath.Base(c) != name {
			t.Errorf("candidate %q does not end with library name %q", c, name)
		}
	}

	// 不应出现重复候选。
	seen := map[string]struct{}{}
	for _, c := range candidates {
		if _, ok := seen[c]; ok {
			t.Errorf("duplicate candidate: %q", c)
		}
		seen[c] = struct{}{}
	}
}

func TestLoadLibraryEnvVarMissingFile(t *testing.T) {
	if IsLoaded() {
		t.Skip("library already loaded; cannot test failure path")
	}

	missing := filepath.Join(t.TempDir(), "does-not-exist-lib")
	t.Setenv(EnvLibPath, missing)

	err := LoadLibrary()
	if err == nil {
		t.Fatal("LoadLibrary with nonexistent SD_LIB_PATH should fail")
	}

	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LoadError, got %T: %v", err, err)
	}
	if le.Platform != runtime.GOOS {
		t.Errorf("LoadError.Platform = %q, want %q", le.Platform, runtime.GOOS)
	}
	if le.Arch != runtime.GOARCH {
		t.Errorf("LoadError.Arch = %q, want %q", le.Arch, runtime.GOARCH)
	}
	if le.Candidate != missing {
		t.Errorf("LoadError.Candidate = %q, want %q", le.Candidate, missing)
	}
	if le.EnvPath != missing {
		t.Errorf("LoadError.EnvPath = %q, want %q", le.EnvPath, missing)
	}
	if le.Err == nil {
		t.Error("LoadError should wrap the underlying loader error")
	}
	msg := err.Error()
	for _, frag := range []string{runtime.GOOS, runtime.GOARCH, missing} {
		if !strings.Contains(msg, frag) {
			t.Errorf("error message %q does not contain %q", msg, frag)
		}
	}

	// 失败后不应标记为已加载，库路径仍为空。
	if IsLoaded() {
		t.Error("IsLoaded() = true after failed load")
	}
	if LibraryPath() != "" {
		t.Errorf("LibraryPath() = %q after failed load, want empty", LibraryPath())
	}
}

func TestLoadLibraryDefaultSearchFailure(t *testing.T) {
	if IsLoaded() {
		t.Skip("library already loaded; cannot test failure path")
	}

	// 确保不使用环境变量，并把工作目录切到临时目录以避开仓库内可能存在的库。
	t.Setenv(EnvLibPath, "")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	err = LoadLibrary()
	if err == nil {
		t.Skip("system loader found the library; cannot test default-search failure")
	}
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("expected *LoadError, got %T: %v", err, err)
	}
	if le.EnvPath != "" {
		t.Errorf("EnvPath should be empty, got %q", le.EnvPath)
	}
	if len(le.Searched) == 0 {
		t.Error("LoadError.Searched should list attempted paths")
	}
	if !strings.Contains(err.Error(), runtime.GOOS) {
		t.Errorf("error %q should mention platform", err.Error())
	}
}
