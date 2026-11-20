package inject

import (
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// defaultThemeCSS 是内置的界面优化样式。
//
// 它做的事很有限：修掉"Windows 上字体栈全部落空、最后用微软雅黑"这件事，
// 外加把滚动条收细。不换配色、不动布局——那些属于用户自己的审美，
// 放 ~/.wbmux/theme.css 里追加。
//
//go:embed theme.css
var defaultThemeCSS string

// themeStyleID 是我们注入的 style 标签 id。和胶囊的标记分开，
// 这样主题注入失败不会影响胶囊，反之亦然。
const themeStyleID = "wbmux-theme"

// ThemeCSS 返回"内置 + 用户追加"的完整样式。
//
// 用户层永远追加在内置之后，于是同优先级下用户规则生效——不需要
// !important 拉锯，也不用担心升级覆盖掉个人修改。
func ThemeCSS() string {
	css := defaultThemeCSS
	home, err := os.UserHomeDir()
	if err != nil {
		return css
	}
	userPath := filepath.Join(home, ".wbmux", "theme.css")
	b, err := os.ReadFile(userPath)
	if err != nil || len(b) == 0 {
		return css
	}
	return css + "\n/* ---- 以下来自 " + userPath + " ---- */\n" + string(b)
}

// themeHash 用内容指纹判断样式是否需要重写：
// 3 秒一轮的巡检不应该每次都动 DOM。
func themeHash(css string) string {
	sum := sha1.Sum([]byte(css))
	return hex.EncodeToString(sum[:8])
}

// EnsureTheme 确保客户端里存在 id=wbmux-theme 的 style 且内容最新。
// 返回是否真的写入了（未变化时返回 false，方便调用方跳过日志）。
func EnsureTheme(c *Client, css string) (bool, error) {
	if css == "" {
		return false, nil
	}
	hash := themeHash(css)
	// 用 %q 转义 css，避免引号/反斜杠破坏 JS 字符串。
	expr := fmt.Sprintf(`(function () {
  var id = %q, want = %q, tag = %q;
  var el = document.getElementById(id);
  if (el && el.getAttribute("data-wbmux-hash") === tag) return "same";
  if (!el) {
    el = document.createElement("style");
    el.id = id;
    (document.head || document.documentElement).appendChild(el);
  }
  el.textContent = want;
  el.setAttribute("data-wbmux-hash", tag);
  return "set";
})()`, themeStyleID, css, hash)

	got, err := c.EvalString(expr, 3*time.Second)
	if err != nil {
		return false, err
	}
	return got == "set", nil
}
