// Package browser 用系统浏览器打开一个本地地址。
//
// 之所以不引第三方库：图形界面走的是"内嵌 HTML + 系统浏览器"这条路，
// 就是为了保住零依赖与交叉编译。为了开个浏览器而破坏这个前提并不划算，
// 而各平台的打开方式本身也就一两行。
package browser

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// appModeCmd 是一条"用应用窗口模式打开"的候选命令。
//
// argv 与 need 分开是因为两者不等价：macOS 上真正执行的是 /usr/bin/open，
// 而前提是 /Applications/Google Chrome.app 这个目录存在。只看 argv[0]
// 会把"没装 Chrome"误判成"可以打开"，然后在 Start 之后静默失败。
type appModeCmd struct {
	// argv 是完整命令行，argv[0] 为要执行的程序。
	argv []string
	// need 是必须存在的前提路径；为空表示只要求 argv[0] 可解析。
	need string
}

// Open 打开地址，优先用浏览器自身的应用窗口模式。
//
// 应用窗口模式（Chromium 系的 --app=）没有地址栏、没有标签页、在任务栏里
// 是独立一项，看起来就是一个本地程序——这是选择"内嵌 Web UI"这条路之后，
// 让界面不至于像个网页的关键。没有 Chromium 系浏览器时退回系统默认浏览器，
// 代价只是多了一圈浏览器外壳，功能不受影响。
func Open(url string) error {
	if url == "" {
		return fmt.Errorf("browser: 地址为空")
	}

	for _, cand := range appModeCommands(url) {
		if cand.need != "" && !fileExists(cand.need) {
			continue
		}
		if !resolvable(cand.argv[0]) {
			continue
		}
		cmd := exec.Command(cand.argv[0], cand.argv[1:]...)
		detach(cmd)
		if err := cmd.Start(); err != nil {
			continue // 这一个不行就试下一个，不直接失败
		}
		// 不回收会留下僵尸进程（类 Unix 上）。浏览器本身是脱离的，
		// 这里回收的只是我们直接创建的那一层。
		go func() { _ = cmd.Wait() }()
		return nil
	}

	return openDefault(url)
}

// resolvable 判断一个命令名能不能执行。
//
// 含路径分隔符的按绝对路径查，否则查 PATH——两者不能混：
// 对绝对路径调 LookPath 会额外命中 PATH 里同名的无关程序。
func resolvable(name string) bool {
	if strings.ContainsAny(name, `/\`) {
		return fileExists(name)
	}
	_, err := exec.LookPath(name)
	return err == nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
