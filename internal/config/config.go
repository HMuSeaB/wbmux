// Package config 管理 wbmux 自身的持久化设置。
//
// 所有状态都放在用户主目录下的 ~/.wbmux/，不写入任何客户端目录。
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// DirName 是设置目录名。
const DirName = ".wbmux"

// Config 是持久化设置。
type Config struct {
	// HostVariant 指定用哪一套安装作为宿主程序，取值为 cn 或 intl。
	// 留空时默认 cn。
	HostVariant string `json:"hostVariant,omitempty"`
	// HostExe 是宿主主程序的绝对路径。留空时自动探测。
	HostExe string `json:"hostExe,omitempty"`
	// ExtraEndpoints 会被追加进生成配置的 officialEndpoints。
	ExtraEndpoints []string `json:"extraEndpoints,omitempty"`

	// GUIToken 是图形界面的访问令牌，持久化保存。
	//
	// # 为什么不做成"每次启动随机"
	//
	// 令牌会写进注入到国内客户端的自定义模型条目（作为 API Key）。而客户端
	// 只在启动时读一次 models.json —— 令牌一变，注入的模型立刻变成 403，
	// 用户必须重启客户端。端口同理（URL 里写着端口）。
	// 两者都随机的话，"wbmux 一重启，客户端就得跟着重启"会变成每次都要做的
	// 动作，而用户看到的只是"昨天还好好的，今天又不行了"（2026-09-27 一上午
	// 撞了三次）。
	//
	// # 安全上的代价（有意识地接受）
	//
	// 原来的设计是"令牌每次随机、只在本机回环、外部猜不到"。持久化之后，
	// 一条泄露过的旧令牌仍然有效——所以这个文件必须当凭据看待（见 Save 的
	// 权限处理）。换来的是"重启不再打断使用"。若哪天想收紧，把这一项删掉
	// 即可回到一次性令牌，代价就是客户端要跟着重启。
	GUIToken string `json:"guiToken,omitempty"`
	// GUIAddr 是上次界面实际监听到的地址，下次启动优先沿用它，
	// 好让注入进客户端的 URL 保持有效。端口被占时退回随机端口
	// （那一次仍然需要重启客户端，界面会明说）。
	GUIAddr string `json:"guiAddr,omitempty"`
}

// rootOverride 允许把设置目录临时指向别处，供测试使用。
//
// 存在的理由：runner 与 webui 的测试需要走完整的"生成配置 → 回读校验"路径，
// 若设置目录固定读真实主目录，测试就会写进用户的 ~/.wbmux，还会被
// 用户自己的设置影响。跨包测试访问不到未导出符号，因此这个接缝必须导出。
//
// 生产代码从不设置它，对真实行为没有影响。
var rootOverride string

// SetRoot 把设置目录改到 dir，返回恢复原值的函数。
//
// 仅供测试。调用方必须 defer 恢复，否则会影响同进程内的其它用例。
func SetRoot(dir string) (restore func()) {
	prev := rootOverride
	rootOverride = dir
	return func() { rootOverride = prev }
}

// Dir 返回设置目录，不保证已存在。
func Dir() (string, error) {
	if rootOverride != "" {
		return rootOverride, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定用户主目录: %w", err)
	}
	return filepath.Join(home, DirName), nil
}

// Path 返回设置文件路径。
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// CacheDir 返回生成配置的存放目录。
//
// 与设置文件分开，便于整目录清理而不会误删用户设置。
func CacheDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "generated"), nil
}

// Load 读取设置；文件不存在时返回零值而非错误，
// 让首次运行无需任何初始化步骤。
func Load() (Config, error) {
	p, err := Path()
	if err != nil {
		return Config{}, err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("读取设置失败: %w", err)
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("解析 %s 失败: %w", p, err)
	}
	return c, nil
}

// Save 原子写入设置文件。
func Save(c Config) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建设置目录失败: %w", err)
	}

	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化设置失败: %w", err)
	}
	raw = append(raw, '\n')

	p := filepath.Join(dir, "config.json")
	tmp := p + ".tmp"
	// 0600：这个文件里有界面令牌（见 GUIToken 的注释），按凭据对待。
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写入设置失败: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存设置失败: %w", err)
	}
	// WriteFile 的权限位只对新建文件生效，老文件得显式收一次。
	// Windows 上 chmod 只映射只读位，失败也无所谓。
	_ = os.Chmod(p, 0o600)
	return nil
}
