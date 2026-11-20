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
	// 样式必须在 head 末尾：客户端路由切换会重建 head，把注入的 style 挤到
	// 原生样式前面——优先级随之失效，表现为"字体突然变回去了"。
	// 所以即使内容没变，也要检查位置，被挤走就重新 append（学自船板的
	// ensureStylesAtEndOfHead）。返回 "same" / "set" / "moved"。
	expr := fmt.Sprintf(`(function () {
  var id = %q, want = %q, tag = %q;
  var head = document.head || document.getElementsByTagName("head")[0];
  var el = document.getElementById(id);
  if (!el || el.getAttribute("data-wbmux-hash") !== tag) {
    if (!el) {
      el = document.createElement("style");
      el.id = id;
      (head || document.documentElement).appendChild(el);
    }
    el.textContent = want;
    el.setAttribute("data-wbmux-hash", tag);
    return "set";
  }
  // 内容没变，但位置可能被后插入的样式挤开了：不在 head 末尾就挪回去。
  if (!head || el.parentNode !== head) {
    if (head) head.appendChild(el);
    return "moved";
  }
  for (var sib = el.nextElementSibling; sib; sib = sib.nextElementSibling) {
    if (sib.tagName === "STYLE" || sib.tagName === "LINK") return "moved";
  }
  return "same";
})()`, themeStyleID, css, hash)

	got, err := c.EvalString(expr, 3*time.Second)
	if err != nil {
		return false, err
	}
	return got != "same", nil
}

// 路由钩子：客户端切页（React Router 的 pushState）后，head 可能被重建、
// 节点被替换。3 秒一轮的巡检最长有 3 秒空窗，期间用户会看到样式"闪一下
// 变回原样"。装一套钩子，在路由变化后立刻补一次（学自船板 runtime.js：
// rAF + 120ms + 400ms 三次，覆盖异步渲染）。
const routeHookJS = `(function () {
  if (window.__wbmux_route_hook) return "already";
  window.__wbmux_route_hook = true;

  // 只做轻量的事：让主循环尽快跑一轮，不在这里重算 CSS。
  var kick = function () {
    var seq = [0, 120, 400];
    seq.forEach(function (ms) {
      setTimeout(function () {
        if (window.__wbmux_route_kick__) window.__wbmux_route_kick__();
      }, ms);
    });
  };
  var wrap = function (type) {
    var orig = history[type];
    if (typeof orig !== "function") return;
    history[type] = function () {
      var ret = orig.apply(this, arguments);
      kick();
      return ret;
    };
  };
  wrap("pushState");
  wrap("replaceState");
  window.addEventListener("popstate", kick);
  return "installed";
})()`

// ensureRouteHook 在客户端里安装一次路由钩子。
//
// 钩子只做一件事：路由变化后调 window.__wbmux_route_kick__() 催一轮同步。
// 这里顺手把它接到 Sync 上，于是切页后 0/120/400ms 各补一次，
// 不用等满 3 秒的巡检周期。
func ensureRouteHook(c *Client) error {
	got, err := c.EvalString(routeHookJS, 3*time.Second)
	if err != nil {
		return err
	}
	if got == "installed" || got == "already" {
		// 把"催一轮"绑到一次轻量 Sync：只需要保证样式位置与胶囊标记在。
		_, err := c.EvalString(`(function () {
  if (window.__wbmux_route_kick__) return "bound";
  window.__wbmux_route_kick__ = function () {
    // 只做标记检查：真正的重注入由 Go 侧下一轮 Sync 完成，
    // 页面里不重算 CSS，避免切页瞬间卡顿。
    var el = document.getElementById("wbmux-theme");
    if (el && document.head && el.parentNode === document.head &&
        el !== document.head.lastElementChild) {
      document.head.appendChild(el);
    }
  };
  return "bound";
})()`, 2*time.Second)
		return err
	}
	return nil
}
