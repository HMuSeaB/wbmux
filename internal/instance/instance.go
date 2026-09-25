// Package instance 负责"同一时间只跑一个图形界面"。
//
// # 为什么需要它
//
// 双击 wbmux.exe 每次都新起一个服务、再开一个浏览器窗口。若上一次那个因为
// 任何原因已经死了，用户屏幕上就留着一个长得一模一样的死窗口，新窗口往往
// 还被它挡住——于是"点了没反应 / 还是打不开"就成了最常见的反馈。
//
// 有了它，第二次启动只会把浏览器指向**已经在跑的那个**，界面上永远只有一个
// wbmux 窗口。
//
// # 怎么判断"已经在跑"
//
// 不用 PID：跨平台判活各有一套（Windows 上 os.FindProcess 恒成功，得走
// OpenProcess），而进程被强杀时 PID 还可能被系统复用，判断反而更脆。
// 这里改为**连一下它记下的端口**——能连上就说明服务活着，顺带证明它确实
// 在监听（而不是一个残留的地址）。端口不通就当它死了，直接接管。
//
// 仍然有一个理论上的窗口：两个启动同时发生、都读到"没人跑"。这个竞态代价
// 极小（多开一个窗口），不值得为它引入文件锁。
package instance

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/HMuSeaB/wbmux/internal/config"
)

// fileName 是运行标记的文件名，放在 wbmux 自己的设置目录下。
const fileName = "run.json"

// dialTimeout 是判活时连端口的等待上限。
//
// 目标是本机回环端口，能连上就是毫秒级的事；500 毫秒足够，
// 又不会在"确实没人跑"时让用户干等。
const dialTimeout = 500 * time.Millisecond

// Info 是一个运行中实例的对外信息。
type Info struct {
	// Addr 是监听地址，形如 127.0.0.1:7578。
	Addr string `json:"addr"`
	// URL 是带令牌的完整访问地址，直接丢给浏览器即可。
	URL string `json:"url"`
	// PID 仅供诊断展示，不参与判活。
	PID int `json:"pid"`
	// StartedAt 是启动时刻的 Unix 毫秒。
	StartedAt int64 `json:"startedAt"`
}

// path 返回运行标记的路径。
func path() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fileName), nil
}

// Lookup 检查是否已有实例在跑。没有则返回 false。
//
// 文件损坏、读不动、地址连不上，统统按"没有"处理：这些异常都不该阻止
// 用户打开界面，最坏结果不过是多开一个窗口。
func Lookup() (Info, bool) {
	p, err := path()
	if err != nil {
		return Info{}, false
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return Info{}, false
	}
	var info Info
	if err := json.Unmarshal(raw, &info); err != nil || info.Addr == "" {
		return Info{}, false
	}
	if !alive(info.Addr) {
		return Info{}, false
	}
	return info, true
}

// alive 连一下地址，判断那里是否真有服务在听。
func alive(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// Claim 把自己登记为当前实例。
func Claim(info Info) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}

	// 原子写：先写临时文件再改名，避免读到写了一半的内容。
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写入运行标记失败: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("写入运行标记失败: %w", err)
	}
	return nil
}

// Release 删除运行标记。
//
// 必须先确认标记还是自己的再删：用户可能在上一次异常退出后紧接着开了新实例，
// 此时旧进程的 defer 才跑到，若无条件删除就会把新实例的标记抹掉，
// 下一次启动又会多开一个窗口。
func Release(self Info) {
	p, err := path()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var cur Info
	if json.Unmarshal(raw, &cur) != nil {
		return
	}
	if cur.Addr != self.Addr || cur.PID != self.PID {
		return
	}
	_ = os.Remove(p)
}
