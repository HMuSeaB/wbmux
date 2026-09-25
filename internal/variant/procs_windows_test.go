//go:build windows

package variant

import "testing"

// TestProcsResolve 钉住"把 API 挂在错的 DLL 上"这类错误。
// 见 internal/console 里同名测试的说明。
func TestProcsResolve(t *testing.T) {
	cases := []struct {
		name string
		proc interface{ Find() error }
	}{
		{"RegOpenKeyExW", procRegOpenKeyExW},
		{"RegEnumKeyExW", procRegEnumKeyExW},
		{"RegQueryValueExW", procRegQueryValueExW},
		{"RegCloseKey", procRegCloseKey},
	}
	for _, c := range cases {
		if err := c.proc.Find(); err != nil {
			t.Errorf("%s 解析失败（挂错 DLL 或函数名拼错）: %v", c.name, err)
		}
	}
}
