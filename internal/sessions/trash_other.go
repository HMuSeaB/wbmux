//go:build !windows

// 非 Windows 平台的回收站兜底。
//
// 为什么不做完整实现：wbmux 的图形界面与"清会话"这块只面向 Windows
// （会话来源各家 IDE 在 macOS/Linux 上路径与格式都不同）。但代码要能交叉
// 编译——`GOOS=darwin go vet ./...` 是既有门禁的一部分。
//
// 所以这里显式返回"不支持"，让调用方走"直接删"分支或报错给用户，
// **而不是默默 os.Remove**：在这条路上静默删掉用户的历史会话，
// 比报一个错糟糕得多。
package sessions

import "fmt"

func moveToTrash(path string) error {
	return fmt.Errorf("这个平台没有接回收站，未删除任何文件：%s", path)
}
