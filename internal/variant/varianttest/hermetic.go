package varianttest

import (
	"os"

	"github.com/HMuSeaB/wbmux/internal/variant"
)

// Probe 造一个"字段一定齐全"的探针，把主目录指向 home。
//
// # 为什么需要它（2026-09-29 同一类事故发生了两次）
//
// variant.Probe 是**手写的结构体**，少给任何一个字段都会在该字段被用到的
// 代码路径上空指针崩溃——而崩溃点往往离测试很远：
//
//   - 第一次（2026-09-29 上午）：只给了 Home/Getenv，漏 `ReadFile`，
//     在 `Detect → readLaunchInfo` 崩，本地当场可见；
//   - 第二次（同日下午）：漏 `Exists`，在 `windowsDriveRoots` 崩，
//     **本地全绿、只有 CI 的 macOS/Windows 才炸**——因为本地盘符探测
//     恰好没走到那一步。
//
// 与其在每条测试里手抄六个字段（总有人漏），不如用这个构造器：
// 先拿 `DefaultProbe()` 的全部字段，再只换掉需要隔离的那几个。
// 这样以后 Probe 加字段时这里自动跟上。
//
// 注意它**不隔离进程探测**：`migrate.IsRunning` 仍查真实进程表。
// 这是有意的——"客户端在不在跑"本来就该按真实情况判断。
func Probe(home string) *variant.Probe {
	p := variant.DefaultProbe()
	p.Home = home
	p.GOOS = "windows" // 探测按 Windows 布局走；六平台构建不受影响
	// 目录存在性只认这个假主目录下的东西：否则测试结果会随开发机上
	// 装了什么而变（这正是"本地绿、CI 红"的来源）。
	p.Exists = func(path string) bool {
		if path == "" || !isUnder(path, home) {
			return false
		}
		_, err := os.Stat(path)
		return err == nil
	}
	// 注册表：显式关掉，别让测试去碰真实注册表。
	p.Registry = nil
	return p
}

// isUnder 判断 path 是否落在 root 之下（按路径前缀，够用且不引依赖）。
func isUnder(path, root string) bool {
	if root == "" {
		return false
	}
	return len(path) >= len(root) && path[:len(root)] == root
}
