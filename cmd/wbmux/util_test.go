package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

func TestAbsPathExpandsHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无法确定主目录: %v", err)
	}

	got, err := absPath("~/some/dir")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, filepath.Clean(home)) {
		t.Errorf("absPath(~/some/dir) = %q, 未展开到主目录 %q", got, home)
	}
}

func TestAbsPathRejectsEmpty(t *testing.T) {
	if _, err := absPath(""); err == nil {
		t.Error("空路径应报错")
	}
}

func TestAbsPathMakesRelativeAbsolute(t *testing.T) {
	got, err := absPath("relative/file.json")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("结果不是绝对路径: %q", got)
	}
}

func TestSingleTarget(t *testing.T) {
	id, err := singleTarget([]string{"intl"})
	if err != nil {
		t.Fatal(err)
	}
	if id != variant.Intl {
		t.Errorf("id = %q", id)
	}

	if _, err := singleTarget(nil); err == nil {
		t.Error("缺少目标时应报错")
	}
	if _, err := singleTarget([]string{"cn", "intl"}); err == nil {
		t.Error("多个目标时应报错")
	}
	if _, err := singleTarget([]string{"nonsense"}); err == nil {
		t.Error("无法识别的目标应报错")
	}
}

func TestShortBuild(t *testing.T) {
	if got := shortBuild("37a65c0b1234567"); got != "37a65c0b" {
		t.Errorf("shortBuild = %q", got)
	}
	if got := shortBuild("abc"); got != "abc" {
		t.Errorf("shortBuild = %q", got)
	}
}
