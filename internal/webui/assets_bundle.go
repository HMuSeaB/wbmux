package webui

import (
	"bytes"
	"fmt"
	"sync"
)

var (
	bundleOnce   sync.Once
	bundleCached []byte
	bundleErr    error
)

// jsModuleOrder 定义前端业务功能模块的有顺序拼接清单。
// 保持顺序以保证核心状态优先初始化、各子面板功能完整就绪、启动器最后自执行。
var jsModuleOrder = []string{
	"assets/js/01-core.js",
	"assets/js/02-overview.js",
	"assets/js/03-providers.js",
	"assets/js/04-ccswitch.js",
	"assets/js/05-tools.js",
	"assets/js/06-growth.js",
	"assets/js/07-workspace.js",
	"assets/js/08-usage.js",
	"assets/js/09-app.js",
	"assets/js/10-audit.js",
}

// RenderIndexPage 组合嵌入式 HTML 骨架、CSS 样式表与 JS 模块，
// 生成运行期完整的单页 WebUI，同时保证开发态多文件清晰解耦与生产态零额外网络请求。
func RenderIndexPage() ([]byte, error) {
	bundleOnce.Do(func() {
		bundleCached, bundleErr = buildIndexPage()
	})
	return bundleCached, bundleErr
}

func buildIndexPage() ([]byte, error) {
	htmlBytes, err := assets.ReadFile("assets/index.html")
	if err != nil {
		return nil, fmt.Errorf("读取 index.html 骨架失败: %w", err)
	}

	cssBytes, err := assets.ReadFile("assets/css/style.css")
	if err != nil {
		return nil, fmt.Errorf("读取 style.css 失败: %w", err)
	}

	var jsBuf bytes.Buffer
	jsBuf.WriteString("(function () {\n  \"use strict\";\n\n")
	for _, modPath := range jsModuleOrder {
		modBytes, err := assets.ReadFile(modPath)
		if err != nil {
			return nil, fmt.Errorf("读取 JS 模块 %s 失败: %w", modPath, err)
		}
		jsBuf.Write(modBytes)
		jsBuf.WriteString("\n\n")
	}
	jsBuf.WriteString("})();\n")

	styleTag := append(append([]byte("<style>\n"), cssBytes...), []byte("\n</style>")...)
	scriptTag := append(append([]byte("<script>\n"), jsBuf.Bytes()...), []byte("\n</script>")...)

	out := bytes.Replace(htmlBytes, []byte("<!-- WB_STYLE -->"), styleTag, 1)
	out = bytes.Replace(out, []byte("<!-- WB_SCRIPTS -->"), scriptTag, 1)

	return out, nil
}
