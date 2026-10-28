package webui

import (
	"strings"
	"testing"
)

// TestDescribeAuthHeaderNeverLeaksToken 是本文件存在的底线：
// 描述里的任何内容都**不能**包含凭据原文，否则贴 issue 即泄号。
func TestDescribeAuthHeaderNeverLeaksToken(t *testing.T) {
	secret := "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1aWQiOiJhYmMiLCJleHAiOjF9.SIGNATURE_PART_HERE"

	got := describeAuthHeader("Bearer "+secret, "")
	if strings.Contains(got, secret) {
		t.Fatalf("描述里出现了完整 token，泄露！%s", got)
	}
	// 连部分片段也不该出现——JWT 的 header 段人人都一样，但 payload 是敏感的。
	if strings.Contains(got, "SIGNATURE_PART_HERE") || strings.Contains(got, "eyJ1aWQi") {
		t.Fatalf("描述里出现了 token 片段，泄露！%s", got)
	}
	// 但要能看出形态与指纹，否则这个诊断没意义。
	for _, want := range []string{"来源=Authorization", "带Bearer", "长度=", "形似JWT", "指纹="} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少必要诊断信息 %q：%s", want, got)
		}
	}
}

// TestDescribeAuthHeaderDetectsCommonMistakes 常见手误要能一眼看出来。
func TestDescribeAuthHeaderDetectsCommonMistakes(t *testing.T) {
	cases := []struct{ name, auth, want string }{
		{"多余空格", "Bearer  0e82268d26d07b37330d2c2ac261b9cd", "⚠内部有空格"},
		{"首尾空白", "Bearer 0e82268d26d07b37330d2c2ac261b9cd ", "⚠首尾有空白"},
		{"粘了引号", `Bearer "0e82268d26d07b37330d2c2ac261b9cd"`, "⚠含引号"},
	}
	for _, c := range cases {
		got := describeAuthHeader(c.auth, "")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s：期望含 %q，得到 %q", c.name, c.want, got)
		}
	}
}

// TestDescribeAuthHeaderEmpty 完全没带凭据时要如实说，不能空着。
func TestDescribeAuthHeaderEmpty(t *testing.T) {
	if got := describeAuthHeader("", ""); got != "未提供任何凭据头" {
		t.Errorf("空凭据描述不对：%q", got)
	}
}

// TestTokenFingerprintDistinguishes 指纹要能区分不同 token，且不可逆。
func TestTokenFingerprintDistinguishes(t *testing.T) {
	a := tokenFingerprint("token-aaa")
	b := tokenFingerprint("token-bbb")
	if a == b {
		t.Error("不同 token 指纹相同，诊断会失去意义")
	}
	if len(a) != 8 {
		t.Errorf("指纹长度应为 8，得到 %d", len(a))
	}
	if tokenFingerprint("") != "空" {
		t.Error("空 token 应有明确标记")
	}
}

// TestRedactSecretText 上游回显的凭据必须被抹掉。
func TestRedactSecretText(t *testing.T) {
	secret := "eyJhbGciOiJSUzI1NiJ9.eyJ1aWQiOiJ4eXoifQ.abcdefgHIJKLMN"
	cases := []string{
		`{"error":"invalid token: ` + secret + `"}`,
		`authorization header: Bearer ` + secret,
		`token=` + secret + ` is expired`,
	}
	for _, raw := range cases {
		got := redactSecretText(raw)
		if strings.Contains(got, "eyJ1aWQi") || strings.Contains(got, "abcdefgHIJKLMN") {
			t.Errorf("脱敏后仍含凭据片段：%s → %s", raw, got)
		}
		if !strings.Contains(got, "已脱敏") {
			t.Errorf("应留下脱敏标记：%s", got)
		}
	}
}

// TestRedactSecretTextKeepsNormalText 别误伤正常报错文本。
func TestRedactSecretTextKeepsNormalText(t *testing.T) {
	normal := "当前账号类型暂不支持该模型"
	if got := redactSecretText(normal); got != normal {
		t.Errorf("正常文本被改动：%q → %q", normal, got)
	}
	shortJWT := "eyJs" // 太短，不像凭据
	if got := redactSecretText(shortJWT); got != shortJWT {
		t.Errorf("短串不该被误伤：%q → %q", shortJWT, got)
	}
}
