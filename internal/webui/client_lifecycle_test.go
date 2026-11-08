package webui

import (
	"testing"

	"github.com/HMuSeaB/wbmux/internal/clientproc"
)

func TestClientsStatusAPI(t *testing.T) {
	e := newTestEnv(t, Options{})
	cleanup := clientproc.SetInspectHookForTest(func(name string) (clientproc.Status, bool) {
		if name == "WorkBuddy.exe" {
			return clientproc.Status{Running: true, PIDs: []int{8888}}, true
		}
		if name == "WorkBuddyAI.exe" {
			return clientproc.Status{Running: false}, true
		}
		return clientproc.Status{}, false
	})
	defer cleanup()

	resp := e.do(t, "GET", "/api/clients/status", "", true)
	if resp.code != 200 {
		t.Fatalf("应返回 200，实际 %d：%s", resp.code, resp.body)
	}

	var out struct {
		Clients map[string]ClientProcessStatus `json:"clients"`
	}
	resp.decode(t, &out)

	cn, ok := out.Clients["cn"]
	if !ok || !cn.Running || len(cn.PIDs) == 0 || cn.PIDs[0] != 8888 {
		t.Errorf("国内版进程状态不符合预期：%+v", cn)
	}

	intl, ok := out.Clients["intl"]
	if !ok || intl.Running {
		t.Errorf("国际版进程状态不符合预期：%+v", intl)
	}
}

func TestLaunchBothSmartAdaptiveMode(t *testing.T) {
	e := newTestEnv(t, Options{})

	// 模拟国内端正在运行（PID: 1001），国际端未运行
	cleanup := clientproc.SetInspectHookForTest(func(name string) (clientproc.Status, bool) {
		if name == "WorkBuddy.exe" {
			return clientproc.Status{Running: true, PIDs: []int{1001}}, true
		}
		return clientproc.Status{Running: false}, false
	})
	defer cleanup()

	resp := e.do(t, "POST", "/api/launch-both", "{}", true)
	if resp.code != 200 {
		t.Fatalf("应返回 200，实际 %d：%s", resp.code, resp.body)
	}

	var out struct {
		Results []struct {
			Side           string `json:"side"`
			OK             bool   `json:"ok"`
			AlreadyRunning bool   `json:"alreadyRunning"`
			PIDs           []int  `json:"pids"`
			Err            string `json:"err"`
		} `json:"results"`
	}
	resp.decode(t, &out)

	if len(out.Results) != 2 {
		t.Fatalf("应返回 2 侧结果，实际 %d", len(out.Results))
	}

	resMap := make(map[string]struct {
		Side           string `json:"side"`
		OK             bool   `json:"ok"`
		AlreadyRunning bool   `json:"alreadyRunning"`
		PIDs           []int  `json:"pids"`
		Err            string `json:"err"`
	})
	for _, r := range out.Results {
		resMap[r.Side] = r
	}

	cn := resMap["cn"]
	if !cn.OK || !cn.AlreadyRunning || len(cn.PIDs) != 1 || cn.PIDs[0] != 1001 {
		t.Errorf("国内端应识别为已运行并跳过启动：%+v", cn)
	}

	intl := resMap["intl"]
	if intl.AlreadyRunning {
		t.Errorf("国际端不应标记为 AlreadyRunning：%+v", intl)
	}
	// 在 mock 环境下没有实际可执行文件，所以 intl 会启动失败并给出 Err
	if intl.OK {
		t.Errorf("国际端在 mock 环境下不应为 OK")
	}
}

func TestTerminateClientAPI(t *testing.T) {
	e := newTestEnv(t, Options{})

	// 目标无效
	respBad := e.do(t, "POST", "/api/terminate-client", `{"target":"unknown"}`, true)
	if respBad.code == 200 {
		t.Fatalf("未知目标应报错，实际 %d", respBad.code)
	}

	// 目标有效且没有在运行的进程（Terminate 报告 0 个并返回 ok）
	respOK := e.do(t, "POST", "/api/terminate-client", `{"target":"cn"}`, true)
	if respOK.code != 200 {
		t.Fatalf("合法请求应返回 200，实际 %d：%s", respOK.code, respOK.body)
	}

	var out struct {
		OK     bool   `json:"ok"`
		Target string `json:"target"`
		Killed int    `json:"killed"`
	}
	respOK.decode(t, &out)
	if !out.OK || out.Target != "cn" {
		t.Errorf("响应不符合预期：%+v", out)
	}
}

