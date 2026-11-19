package webui

import (
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/HMuSeaB/wbmux/internal/embedstats"
)

// TestEmbedHTMLThresholds 锁死胶囊与面板的同一口径：上下文告警阈值
// （256k/512k）与 stale 分档（30/120 分钟）的唯一来源是 Go 端
// internal/embedstats，embed.html 里的数字必须与之一致。
// 两边各写一份硬编码是漂移过的（2026-11-19 之前就是两份），
// 改一边忘另一边时这个测试先炸。
func TestEmbedHTMLThresholds(t *testing.T) {
	data, err := assets.ReadFile("assets/embed.html")
	if err != nil {
		t.Fatalf("assets/embed.html 缺失: %v", err)
	}
	htmlStr := string(data)
	for _, want := range []string{
		strconv.Itoa(embedstats.ContextWarnTokens),        // 256000
		strconv.Itoa(embedstats.ContextBadTokens),         // 512000
		strconv.Itoa(int(embedstats.AgedAfter.Seconds())), // 1800
		strconv.Itoa(int(embedstats.IdleAfter.Seconds())), // 7200
	} {
		if !strings.Contains(htmlStr, want) {
			t.Errorf("embed.html 缺少与 Go 端 internal/embedstats 一致的阈值 %q", want)
		}
	}
	// 新鲜度三档与来源标注、额度条的渲染入口必须存在：删了其中一个，
	// 面板就悄悄退回"二值 stale / 无来源 / 无额度"的旧行为。
	for _, sym := range []string{"STALE_AGED_SEC", "STALE_IDLE_SEC", "sourceLabel", "setSrcLine", "visibilitychange", "renderQuota"} {
		if !strings.Contains(htmlStr, sym) {
			t.Errorf("embed.html 缺少关键符号 %q", sym)
		}
	}
}

// TestIndexHTMLValidity 作为持续集成与防呆防护用例：
// 检验 embedded 模块化前端资源完整性、RenderIndexPage 聚合输出以及脚本语法的健全性，
// 杜绝任何因代码合并截断或未闭合括号/引号导致的 WebUI 全局崩溃。
func TestIndexHTMLValidity(t *testing.T) {
	// 1. 验证各模块化文件独立存在且体积正常
	cssData, err := assets.ReadFile("assets/css/style.css")
	if err != nil || len(cssData) < 10000 {
		t.Fatalf("assets/css/style.css 缺失或体积异常: %v (%d bytes)", err, len(cssData))
	}

	for _, mod := range jsModuleOrder {
		data, err := assets.ReadFile(mod)
		if err != nil || len(data) < 100 {
			t.Errorf("JS 模块 %s 缺失或内容为空: %v", mod, err)
		}
	}

	// 2. 验证聚合渲染器输出
	content, err := RenderIndexPage()
	if err != nil {
		t.Fatalf("RenderIndexPage 聚合失败: %v", err)
	}

	htmlStr := string(content)
	if len(htmlStr) < 50000 {
		t.Fatalf("聚合后页面体积异常偏小 (%d bytes)", len(htmlStr))
	}

	// 3. 验证关键函数入口与核心逻辑完整存在
	criticalSymbols := []string{
		"function showCCSwitchExportModal",
		"function showCCSwitchImportModal",
		"function renderChatProxy",
		"function renderUsage",
		"function loadPreview",
		"function applyCNFilter",
		"function updateTelemetryBadge",
	}
	for _, sym := range criticalSymbols {
		if !strings.Contains(htmlStr, sym) {
			t.Errorf("缺少关键前端符号或函数入口: %q", sym)
		}
	}

	// 4. 提取聚合后的 <style> 块并进行语法与嵌套防呆校验
	reStyle := regexp.MustCompile(`(?is)<style\b[^>]*>(.*?)</style>`)
	styleMatches := reStyle.FindAllStringSubmatch(htmlStr, -1)
	if len(styleMatches) != 1 {
		t.Fatalf("聚合 HTML 中的 <style> 标签数量异常: 预期 1 个，实际发现 %d 个", len(styleMatches))
	}
	styleBody := styleMatches[0][1]
	if strings.Contains(styleBody, "<style") || strings.Contains(styleBody, "</style>") {
		t.Fatalf("style 块内存在非法嵌套的 <style> 标签，会导致浏览器 CSS 解析器抛弃 :root 规则")
	}
	if strings.Count(styleBody, "{") != strings.Count(styleBody, "}") {
		t.Errorf("style 块花括号不平衡: '{'=%d, '}'=%d", strings.Count(styleBody, "{"), strings.Count(styleBody, "}"))
	}
	for _, reqVar := range []string{"--g0:", "--g1:", "--ink:", ".sidebar", "background: var(--g1)"} {
		if !strings.Contains(styleBody, reqVar) {
			t.Errorf("style 块缺少关键变量或规则声明: %q", reqVar)
		}
	}

	// 5. 提取聚合后的 <script> 块并进行语法健全度校验
	reScript := regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script>`)
	matches := reScript.FindAllStringSubmatch(htmlStr, -1)
	if len(matches) == 0 {
		t.Fatalf("未在聚合 HTML 中发现 <script> 标签")
	}

	for idx, m := range matches {
		scriptBody := m[1]
		// 括号平衡检查
		openParens := strings.Count(scriptBody, "(")
		closeParens := strings.Count(scriptBody, ")")
		if openParens != closeParens {
			t.Errorf("script[%d] 圆括号不平衡: '('=%d, ')'=%d", idx, openParens, closeParens)
		}

		openBraces := strings.Count(scriptBody, "{")
		closeBraces := strings.Count(scriptBody, "}")
		if openBraces != closeBraces {
			t.Errorf("script[%d] 花括号不平衡: '{'=%d, '}'=%d", idx, openBraces, closeBraces)
		}

		openBrackets := strings.Count(scriptBody, "[")
		closeBrackets := strings.Count(scriptBody, "]")
		if openBrackets != closeBrackets {
			t.Errorf("script[%d] 方括号不平衡: '['=%d, ']'=%d", idx, openBrackets, closeBrackets)
		}
	}

	// 5. 若宿主环境安装有 Node.js，通过 stdin 对聚合后的代码执行精准 AST 编译校验
	nodePath, err := exec.LookPath("node")
	if err == nil && nodePath != "" {
		for idx, m := range matches {
			cmd := exec.Command(nodePath, "-e", `
const vm = require('vm');
let code = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', chunk => code += chunk);
process.stdin.on('end', () => {
  try {
    new vm.Script(code);
  } catch(e) {
    console.error(e.message);
    process.exit(1);
  }
});
`)
			cmd.Stdin = strings.NewReader(m[1])
			out, nodeErr := cmd.CombinedOutput()
			if nodeErr != nil {
				t.Fatalf("script[%d] Node.js 严格语法编译校验失败: %v, 输出: %s", idx, nodeErr, string(out))
			}
		}
	}
}
