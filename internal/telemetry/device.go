// Package telemetry 负责客户端指纹持久化、高仿真 User-Agent 构造与反风控心跳守护。
package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	deviceIDMu   sync.Mutex
	cachedDevice string
)

// DeviceID 返回本机唯一的持久化客户端指纹（32 位 hex）。
//
// 机制：
//  1. 优先读取环境变量 WBMUX_DEVICE_ID（供测试与高级用户覆盖）；
//  2. 尝试从 ~/.wbmux/device.id 读取已持久化的 ID；
//  3. 若不存在，根据系统特征（计算机名、用户名、系统架构、用户主目录）计算 SHA-256，
//     并持久化落盘至 ~/.wbmux/device.id，确保同一机器在服务端的设备标识持久一致。
func DeviceID(homeDir string) string {
	deviceIDMu.Lock()
	defer deviceIDMu.Unlock()

	if cachedDevice != "" {
		return cachedDevice
	}

	if env := strings.TrimSpace(os.Getenv("WBMUX_DEVICE_ID")); env != "" {
		cachedDevice = env
		return cachedDevice
	}

	if homeDir == "" {
		homeDir, _ = os.UserHomeDir()
	}

	if homeDir != "" {
		targetFile := filepath.Join(homeDir, ".wbmux", "device.id")
		if raw, err := os.ReadFile(targetFile); err == nil {
			id := strings.TrimSpace(string(raw))
			if len(id) >= 16 {
				cachedDevice = id
				return cachedDevice
			}
		}
	}

	// 生成稳定哈希
	id := generateStableMachineID(homeDir)
	if homeDir != "" {
		targetFile := filepath.Join(homeDir, ".wbmux", "device.id")
		_ = os.MkdirAll(filepath.Dir(targetFile), 0o755)
		_ = os.WriteFile(targetFile, []byte(id), 0o600)
	}

	cachedDevice = id
	return cachedDevice
}

func generateStableMachineID(homeDir string) string {
	h := sha256.New()
	hostname, _ := os.Hostname()
	h.Write([]byte(hostname))
	h.Write([]byte(runtime.GOOS))
	h.Write([]byte(runtime.GOARCH))
	h.Write([]byte(homeDir))

	// 补充系统特有标识
	if runtime.GOOS == "windows" {
		h.Write([]byte(os.Getenv("COMPUTERNAME")))
		h.Write([]byte(os.Getenv("USERDOMAIN")))
		h.Write([]byte(os.Getenv("PROCESSOR_IDENTIFIER")))
	} else {
		h.Write([]byte(os.Getenv("USER")))
	}

	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:32]
}

// ResetCacheForTest 仅供单元测试重置内存缓存。
func ResetCacheForTest() {
	deviceIDMu.Lock()
	defer deviceIDMu.Unlock()
	cachedDevice = ""
}
