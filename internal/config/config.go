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
}

// Dir 返回设置目录，不保证已存在。
func Dir() (string, error) {
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
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return fmt.Errorf("写入设置失败: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("保存设置失败: %w", err)
	}
	return nil
}
