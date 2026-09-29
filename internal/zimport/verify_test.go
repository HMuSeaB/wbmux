package zimport

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant/varianttest"
)

// TestHermeticProbeIsFieldComplete 直接钉住"探针字段齐全"这件事。
//
// 这条测试的价值在于：它**不依赖具体哪条路径被走到**。手抄探针的坑是
// "本地绿、CI 红"（2026-09-29 第二次踩到时，本地恰好没走到 windowsDriveRoots
// 的 Exists 分支）。所以这里把 Probe 的每个字段逐个点名调用一遍——
// 少任何一个都会在这里当场崩，而不是等 CI 跑到某条特定路径上才发现。
func TestHermeticProbeIsFieldComplete(t *testing.T) {
	home := t.TempDir()
	p := varianttest.Probe(home)

	// 逐个字段点名：任一为 nil 都会在这行崩掉（而不是在别的包的深处）。
	if p.Getenv == nil {
		t.Fatal("Getenv 为 nil")
	}
	if p.Exists == nil {
		t.Fatal("Exists 为 nil")
	}
	if p.ReadFile == nil {
		t.Fatal("ReadFile 为 nil")
	}
	if p.Home == "" {
		t.Fatal("Home 为空")
	}
	if p.GOOS == "" {
		t.Fatal("GOOS 为空")
	}

	// 真的调用一遍：Exists 要能正常回答（不 panic 且对假目录说 false）。
	_ = p.Getenv("PATH")
	if p.Exists(`C:/Windows`) {
		t.Error("隔离探针不该认出真实机器的盘符目录（否则测试结果随开发机而变）")
	}
	// 它自己造出来的路径要认得。
	probeFile := filepath.Join(home, "probe-check.txt")
	if err := os.WriteFile(probeFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !p.Exists(probeFile) {
		t.Error("隔离探针应当能看见自己主目录下的文件")
	}
	_ = p.ReadFile
}
