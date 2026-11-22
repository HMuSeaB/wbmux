package inject

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PanelVersion 是注入脚本的版本号。
//
// 守护进程靠它判断"页面里那套面板是不是当前这版"：脚本改动后把版本 +1，
// 客户端里已注入的旧面板会在 3 秒内被自动替换，不需要用户刷新窗口。
//
// v17：额度行显示 剩余/总量，不再只有百分比。
// v21：胶囊配色从旧青绿统一到面板暖色（陶土橙 #D97757 系），
// 与 2026-11-20/21 面板换暖色是同一决策。
const PanelVersion = "21"

// TagFor 返回某个 embed 地址对应的注入标记。
//
// 标记里带上完整 URL 有两个作用：一是面板升级（版本号变）能触发重注入；
// 二是 wbmux 重启后令牌会变，旧 iframe 指向的地址失效——标记对不上，
// 守护就会把新令牌的地址重新推一遍，用户无感。
func TagFor(embedURL string) string {
	return PanelVersion + "|" + embedURL
}

// panelScriptTemplate 是注入到 WorkBuddy 渲染进程的挂载脚本。
//
// 布局约束（改之前先读）：
//   - 胶囊放在顶栏右侧（right:160px 避开窗口控制按钮）；
//   - 顶栏在 Electron 里是窗口拖拽区，**必须显式 no-drag**，否则真实鼠标
//     点击会被系统当成拖动标题栏吞掉——CDP 模拟点击能触发，用户点却没反应，
//     2026-11-16 实测踩过这个坑；
//   - 弹层关闭时必须是 display:none + pointer-events:none，只设透明度会留下
//     一块"隐形玻璃板"挡住下面的编辑区；
//   - 重注入前先调用上一版的 cleanup，移除旧节点与事件监听（幂等）。
const panelScriptTemplate = `(() => {
  const EMBED_URL = %%EMBED_JSON%%;
  const TAG = %%TAG_JSON%%;
  const VERSION = %%VERSION_JSON%%;

  if (window.__wbmuxPanelCleanup) { try { window.__wbmuxPanelCleanup(); } catch (e) {} }
  ['wbmux-probe', 'wbmux-probe-card', 'wbmux-panel-badge', 'wbmux-panel-pop'].forEach(id => {
    const n = document.getElementById(id);
    if (n) n.remove();
  });

  const badge = document.createElement('div');
  badge.id = 'wbmux-panel-badge';
  badge.innerHTML = '<span style="width:6px;height:6px;border-radius:50%;background:#D97757;display:inline-block"></span>'
    + '<span>wbmux</span>'
    + '<span id="wbmux-panel-stats" style="color:#9E988D;font-weight:500;max-width:280px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">连接中…</span>';
  Object.assign(badge.style, {
    position: 'fixed', top: '7px', right: '160px', zIndex: 99998,
    display: 'flex', alignItems: 'center', gap: '6px',
    background: '#262422', color: '#C9C4BC',
    border: '1px solid rgba(217,119,87,0.35)', borderRadius: '999px',
    padding: '3px 12px', font: '600 11.5px/1.5 system-ui, sans-serif',
    cursor: 'pointer', userSelect: 'none', pointerEvents: 'auto',
  });
  // 顶栏是窗口拖拽区：不写这两行，真实鼠标点不到（CDP 模拟点击却能）
  badge.style.setProperty('-webkit-app-region', 'no-drag');
  badge.style.setProperty('app-region', 'no-drag');
  badge.title = 'wbmux 本机工作区面板';

  const pop = document.createElement('div');
  pop.id = 'wbmux-panel-pop';
  Object.assign(pop.style, {
    position: 'fixed', top: '42px', right: '16px', zIndex: 99999,
    width: '400px', height: '600px', maxHeight: 'calc(100vh - 60px)',
    borderRadius: '12px', overflow: 'hidden',
    border: '1px solid rgba(217,119,87,0.25)',
    boxShadow: '0 12px 40px rgba(0,0,0,0.55)',
    background: '#1F1E1D',
    display: 'none', pointerEvents: 'none',
  });
  pop.style.setProperty('-webkit-app-region', 'no-drag');

  const frame = document.createElement('iframe');
  // 带版本参数：面板页升级后 src 变化，iframe 会重新请求，不会留在旧页面上。
  frame.src = EMBED_URL + (EMBED_URL.indexOf('?') >= 0 ? '&' : '?') + 'v=' + encodeURIComponent(VERSION);
  frame.setAttribute('allow', 'clipboard-write');
  Object.assign(frame.style, { width: '100%', height: '100%', border: '0', background: '#1F1E1D' });
  pop.appendChild(frame);

  let open = false;
  function setOpen(v) {
    open = v;
    pop.style.display = v ? 'block' : 'none';
    pop.style.pointerEvents = v ? 'auto' : 'none';
    badge.style.borderColor = v ? '#D97757' : 'rgba(217,119,87,0.35)';
  }
  function onDocClick(ev) {
    if (!open) return;
    const t = ev.target;
    if (pop.contains(t) || badge.contains(t)) return;
    setOpen(false);
  }
  function onKey(ev) { if (ev.key === 'Escape' && open) setOpen(false); }

  badge.addEventListener('click', () => setOpen(!open));
  document.addEventListener('click', onDocClick, true);
  document.addEventListener('keydown', onKey, true);

  // 指标更新入口：守护进程每轮通过 CDP 调它，把最新数字推进胶囊。
  // 走函数而不是直接改 DOM，是为了让"面板是否还在"由页面自己判断——
  // 用户刷新过窗口时这里自然返回 false，守护下一轮就会重新注入。
  window.__wbmuxPanelStats = (text, level) => {
    const el = document.getElementById('wbmux-panel-stats');
    if (!el) return false;
    el.textContent = text;
    el.style.color = level === 'bad' ? '#C25B4E' : (level === 'warn' ? '#D9A354' : '#9E988D');
    return true;
  };

  window.__wbmuxPanelCleanup = () => {
    document.removeEventListener('click', onDocClick, true);
    document.removeEventListener('keydown', onKey, true);
    badge.remove();
    pop.remove();
    delete window.__wbmuxPanelCleanup;
    delete window.__wbmuxPanelVersion;
    delete window.__wbmuxPanelStats;
  };

  document.body.appendChild(badge);
  document.body.appendChild(pop);
  window.__wbmuxPanelVersion = TAG;
  return { ok: true, badgeMounted: !!document.getElementById('wbmux-panel-badge') };
})()`

// Inject 把面板推入页面。幂等：重复调用会用新脚本替换旧实例。
func Inject(c *Client, embedURL string, timeout time.Duration) (bool, error) {
	embedJSON, err := json.Marshal(embedURL)
	if err != nil {
		return false, err
	}
	tagJSON, err := json.Marshal(TagFor(embedURL))
	if err != nil {
		return false, err
	}
	script := strings.ReplaceAll(panelScriptTemplate, "%%EMBED_JSON%%", string(embedJSON))
	script = strings.ReplaceAll(script, "%%TAG_JSON%%", string(tagJSON))
	versionJSON, err := json.Marshal(PanelVersion)
	if err != nil {
		return false, err
	}
	script = strings.ReplaceAll(script, "%%VERSION_JSON%%", string(versionJSON))

	raw, err := c.Evaluate(script, timeout)
	if err != nil {
		return false, err
	}
	var out struct {
		OK           bool `json:"ok"`
		BadgeMounted bool `json:"badgeMounted"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return out.OK && out.BadgeMounted, nil
}

// InstalledTag 返回页面中已注入面板的标记；没有则空串。
func InstalledTag(c *Client, timeout time.Duration) (string, error) {
	return c.EvalString(`(window.__wbmuxPanelVersion || '')`, timeout)
}

// UpdateStats 把指标文字推进胶囊。
//
// level 只影响文字颜色："warn" 黄、"bad" 红、其它保持灰。
// 页面里没有胶囊（用户刷新过窗口）时返回 false，不当作错误——
// 守护下一轮会重新注入。
func UpdateStats(c *Client, text, level string, timeout time.Duration) (bool, error) {
	textJSON, err := json.Marshal(text)
	if err != nil {
		return false, err
	}
	levelJSON, err := json.Marshal(level)
	if err != nil {
		return false, err
	}
	expr := fmt.Sprintf(`(window.__wbmuxPanelStats && window.__wbmuxPanelStats(%s, %s)) || false`, textJSON, levelJSON)
	return c.EvalBool(expr, timeout)
}
