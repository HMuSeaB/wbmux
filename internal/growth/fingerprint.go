package growth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"
)

const (
	// DesktopUA 是官方 WorkBuddy 5.5.6 桌面客户端内嵌 CLI 的标准 User-Agent。
	DesktopUA = "WorkBuddy/5.5.6 WorkBuddy/5.5.6 CLI/2.137.1"

	// WebUA 是用于 Web 域事件上报的标准 Chrome 浏览器 User-Agent。
	WebUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36"

	// DesktopReportPath 是桌面端事件上报路由。
	DesktopReportPath = "/v2/report"

	// MPPlatform 是小程序专属平台的标头值。
	MPPlatform = "miniprogram"

	// MPUA 是微信小程序标准移动端 User-Agent。
	MPUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148 MicroMessenger/8.0.50 NetType/WIFI Language/zh_CN"
)

// deriveDeviceID 由账号 UID 稳定派生一个 36 位十六进制设备标识（machineId/sessionId/qimei36）。
// 幂等：同一账号每次调用生成相同值，避免每次随机变化引起设备频繁漂移风控。
func deriveDeviceID(uid, salt string) string {
	if uid == "" {
		uid = "default-anonymous-user"
	}
	sum := sha256.Sum256([]byte(salt + ":" + uid))
	return hex.EncodeToString(sum[:18]) // 36 hex chars
}

// DesktopFingerprint 返回官方桌面端的全套公共指纹字段。
func DesktopFingerprint(uid, nickname string) map[string]any {
	now := time.Now().UnixMilli()
	if nickname == "" {
		nickname = "WorkBuddy"
	}
	return map[string]any{
		"timezone":     "Asia/Shanghai",
		"reportDelay":  2000,
		"userId":       uid,
		"username":     nickname,
		"userNickname": nickname,
		"product":      "SaaS",
		"releaseDate":  int64(1789036585355),
		"commit":       "5f9692923c93033111c51ad7b003eb80204a9b75",
		"ideName":      "WorkBuddy",
		"ideType":      "WorkBuddy",
		"ideVersion":   "5.5.6",
		"machineId":    deriveDeviceID(uid, "machine"),
		"sessionId":    deriveDeviceID(uid, "session"),
		"extName":      "workbuddy-desktop",
		"extVersion":   "5.5.6",
		"os":           "win32",
		"arch":         "x64",
		"osVersion":    "10.0.26220",
		"cpuCores":     20,
		"memorySize":   24,
		"timestamp":    now,
		"presentAt":    now,
	}
}

// BuildDesktopEvents 将公共桌面指纹字段注入到一组事件载荷中。
// 业务事件字段优先（可覆盖 machineId/qimei36 等设备标识）。
func BuildDesktopEvents(uid, nickname string, events ...map[string]any) []map[string]any {
	if len(events) == 0 {
		return nil
	}
	fp := DesktopFingerprint(uid, nickname)
	res := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		m := make(map[string]any, len(fp)+len(ev))
		for k, v := range fp {
			m[k] = v
		}
		for k, v := range ev {
			m[k] = v
		}
		res = append(res, m)
	}
	return res
}

// ApplyDesktopHeaders 为 HTTP 请求注入桌面客户端指纹头。
func ApplyDesktopHeaders(req *http.Request, token, uid, domain string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("User-Agent", DesktopUA)
	if domain != "" {
		req.Header.Set("X-Domain", domain)
	}
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("X-Request-ID", deriveDeviceID(uid, "req")+fmt.Sprintf("%d", time.Now().UnixNano()%1e6))
	if uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
}

// ApplyWebHeaders 为 HTTP 请求注入 Web 域客户端指纹头。
func ApplyWebHeaders(req *http.Request, token, uid, domain, pageURL string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-client-platform", "web")
	if domain != "" {
		req.Header.Set("Origin", domain)
	}
	if pageURL != "" {
		req.Header.Set("Referer", pageURL)
	}
	req.Header.Set("User-Agent", WebUA)
	if uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
}

// ApplyMPHeaders 为 HTTP 请求注入小程序专属口径头。
func ApplyMPHeaders(req *http.Request, token, uid string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", MPUA)
	req.Header.Set("X-Client-Platform", MPPlatform)
	if uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
}

// MPFingerprint 返回微信小程序公共指纹字段。
func MPFingerprint(uid string) map[string]any {
	now := time.Now().UnixMilli()
	return map[string]any{
		"userId":      uid,
		"timestamp":   now,
		"presentAt":   now,
		"platform":    "miniprogram",
		"source":      "mini_program",
		"machineId":   deriveDeviceID(uid, "mpmachine"),
		"sessionId":   deriveDeviceID(uid, "mpsession"),
		"extVersion":  "2.2.8",
		"os":          "ios",
		"osVersion":   "iOS 17.5.1",
		"deviceModel": "iPhone 15 Pro",
		"product":     "SaaS",
	}
}

// BuildMPEvents 将小程序公共指纹注入到一组事件载荷中。
func BuildMPEvents(uid string, events ...map[string]any) []map[string]any {
	if len(events) == 0 {
		return nil
	}
	fp := MPFingerprint(uid)
	res := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		m := make(map[string]any, len(fp)+len(ev))
		for k, v := range fp {
			m[k] = v
		}
		for k, v := range ev {
			m[k] = v
		}
		res = append(res, m)
	}
	return res
}

// BuildWebEvent 构造 Web 端元素点击等遥测事件结构。
func BuildWebEvent(uid, nickname, eventCode, pageURL, elementID, elementName string) map[string]any {
	return map[string]any{
		"eventCode":    eventCode,
		"timestamp":    time.Now().UnixMilli(),
		"reportDelay":  0,
		"pageURL":      pageURL,
		"elementId":    elementID,
		"elementName":  elementName,
		"os":           "Win32",
		"arch":         "",
		"osVersion":    "10.0",
		"userAgent":    WebUA,
		"machineId":    deriveDeviceID(uid, "webmachine"),
		"userId":       uid,
		"userNickname": nickname,
	}
}
