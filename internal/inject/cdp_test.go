package inject

import "testing"

// TestIsHostPage 钉住注入目标白名单：只认 file:// 下的客户端主界面，
// 内置浏览器与本地预览页一律不碰。
func TestIsHostPage(t *testing.T) {
	ws := "ws://127.0.0.1:9223/devtools/page/abc"
	cases := []struct {
		name string
		t    pageTarget
		want bool
	}{
		{"主界面", pageTarget{Type: "page", URL: "file:///D:/Tools/WorkBuddy/resources/app.asar/renderer/index.html?locale=zh-CN", WebSocketDebuggerURL: ws}, true},
		{"内置浏览器", pageTarget{Type: "page", URL: "https://example.com/a", WebSocketDebuggerURL: ws}, false},
		{"本地预览", pageTarget{Type: "page", URL: "http://localhost:3000/", WebSocketDebuggerURL: ws}, false},
		{"我们的 iframe", pageTarget{Type: "iframe", URL: "http://127.0.0.1:35907/embed?t=x", WebSocketDebuggerURL: ws}, false},
		{"无调试地址", pageTarget{Type: "page", URL: "file:///D:/Tools/WorkBuddy/resources/app.asar/renderer/index.html"}, false},
		{"无关本地文件", pageTarget{Type: "page", URL: "file:///C:/Users/x/notes.html", WebSocketDebuggerURL: ws}, false},
	}
	for _, c := range cases {
		if got := isHostPage(c.t); got != c.want {
			t.Errorf("%s: isHostPage = %v, want %v", c.name, got, c.want)
		}
	}
}
