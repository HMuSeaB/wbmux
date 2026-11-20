package launch

import (
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// TestBuildArgsRequestsEphemeralPort 验证启动参数请求的是 0（Chromium 自选端口）。
//
// 固定端口在别的程序占用时只能放弃注入，而 0 永远能绑上；实际端口由
// Chromium 写进 <dataDir>/DevToolsActivePort，供 inject.DiscoverPorts 读取。
func TestBuildArgsRequestsEphemeralPort(t *testing.T) {
	args := buildArgs(variant.CN, `C:\ud`)
	var hasPort0, hasDataDir bool
	for _, a := range args {
		if a == "--remote-debugging-port=0" {
			hasPort0 = true
		}
		if strings.HasPrefix(a, UserDataDirFlag+"=") {
			hasDataDir = true
		}
	}
	if !hasPort0 {
		t.Fatalf("启动参数应请求 --remote-debugging-port=0: %v", args)
	}
	if !hasDataDir {
		t.Fatalf("启动参数缺少数据目录: %v", args)
	}
}

// TestBuildArgsNoFixedPort 确保不再出现写死端口的参数——那会把"端口被占"
// 这个老问题带回来。
func TestBuildArgsNoFixedPort(t *testing.T) {
	for _, id := range []variant.ID{variant.CN, variant.Intl} {
		for _, a := range buildArgs(id, `C:\ud`) {
			if strings.HasPrefix(a, "--remote-debugging-port=") && a != "--remote-debugging-port=0" {
				t.Fatalf("%s 档位出现写死端口: %v", id, a)
			}
		}
	}
}
