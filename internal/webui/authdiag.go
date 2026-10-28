package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
)

// 认证诊断：把"凭据形态"记进日志，用来排查 401。
//
// # 为什么需要它
//
// 用户报 401 时，我们离真相只差一件事：**请求到底带了什么凭据、
// 上游怎么回的**。而这两样都不能原样写进日志——日志是要贴到公开
// issue 里的，JWT 一贴出去账号就废了。
//
// 所以这里只记"形态"，不记"内容"：
//   - 来源（Authorization / x-api-key / 无）
//   - 是否带 Bearer 前缀、有没有多余空格
//   - 长度、是否 JWT 形状（三段）
//   - **指纹**：SHA-256 前 8 位。它不可逆，但足以回答
//     "客户端发的和 wbmux 手里的是不是同一个 token"
//
// 上游返回体同样过一遍 redactSecretText，把可能混进去的 token 抹掉。
//
// # 开关
//
// 默认**一直开启**：这些信息本身就是脱敏的，留着对排查有利。
// 若嫌日志吵，把 WBMUX_AUTH_DIAG 设为 "0" 即可关闭。

// authDiagEnabled 报告是否记录认证诊断。
func authDiagEnabled() bool {
	v := strings.TrimSpace(os.Getenv("WBMUX_AUTH_DIAG"))
	return v != "0" && !strings.EqualFold(v, "off") && !strings.EqualFold(v, "false")
}

// tokenFingerprint 给凭据算一个可公开的指纹。
//
// 取 SHA-256 前 8 个 hex 字符（32 bit）。足够区分不同的 token，
// 又完全无法反推出原文——这是本函数存在的唯一理由。
func tokenFingerprint(tok string) string {
	if tok == "" {
		return "空"
	}
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:4])
}

// describeAuthHeader 描述一条凭据的形态，**绝不包含原文**。
//
// 返回值示例：
//
//	"来源=Authorization 带Bearer 长度=1381 形似JWT 指纹=a1b2c3d4"
//	"来源=x-api-key 无Bearer 长度=32 指纹=0e82268d"
//	"未提供"
func describeAuthHeader(authorization, apiKey string) string {
	var parts []string
	switch {
	case strings.TrimSpace(authorization) != "":
		parts = append(parts, "来源=Authorization")
		raw := authorization
		if strings.HasPrefix(raw, "Bearer ") {
			parts = append(parts, "带Bearer")
			raw = strings.TrimPrefix(raw, "Bearer ")
		} else if strings.HasPrefix(strings.ToLower(raw), "bearer ") {
			parts = append(parts, "带Bearer(小写)")
			raw = raw[len("bearer "):]
		} else {
			parts = append(parts, "无Bearer前缀")
		}
		// 常见手误：多打了空格，或把整段连引号粘进来。
		if raw != strings.TrimSpace(raw) {
			parts = append(parts, "⚠首尾有空白")
		}
		if strings.ContainsAny(raw, "\"'") {
			parts = append(parts, "⚠含引号")
		}
		if strings.Contains(raw, " ") {
			parts = append(parts, "⚠内部有空格")
		}
		tok := strings.TrimSpace(raw)
		parts = append(parts, "长度="+itoa(len(tok)))
		if strings.Count(tok, ".") == 2 {
			parts = append(parts, "形似JWT")
		}
		parts = append(parts, "指纹="+tokenFingerprint(tok))
	case strings.TrimSpace(apiKey) != "":
		tok := strings.TrimSpace(apiKey)
		parts = append(parts, "来源=x-api-key", "长度="+itoa(len(tok)),
			"指纹="+tokenFingerprint(tok))
	default:
		return "未提供任何凭据头"
	}
	return strings.Join(parts, " ")
}

// itoa 避免为一个数字引入 strconv（本文件其余地方用不到）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// redactSecretText 把上游响应里可能回显的凭据抹掉。
//
// 上游报错时偶尔会把 Authorization 原样带回来（"invalid token: eyJ..."）。
// 这类内容一旦进日志就等于泄露，所以按形状做兜底替换：
//   - JWT 形状（eyJ... 开头、三段点分）
//   - Bearer 后面的任意长串
func redactSecretText(s string) string {
	if s == "" {
		return s
	}
	// Bearer xxx → Bearer <已脱敏>
	if i := strings.Index(s, "Bearer "); i >= 0 {
		start := i + len("Bearer ")
		end := start
		for end < len(s) {
			c := s[end]
			if c == ' ' || c == '"' || c == '\'' || c == ',' || c == '}' || c == '\n' || c == '\r' {
				break
			}
			end++
		}
		if end > start+8 {
			s = s[:start] + "…已脱敏" + s[end:]
		}
	}
	// eyJ 开头的 JWT（可能不带 Bearer 前缀）
	for {
		i := strings.Index(s, "eyJ")
		if i < 0 {
			break
		}
		end := i
		for end < len(s) {
			c := s[end]
			if c == ' ' || c == '"' || c == '\'' || c == ',' || c == '}' || c == '\n' || c == '\r' {
				break
			}
			end++
		}
		if end-i < 16 {
			// 太短，不像真凭据，别误伤正常文本
			break
		}
		s = s[:i] + "…已脱敏" + s[end:]
	}
	return s
}
