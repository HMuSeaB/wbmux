package tray

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestICOContainerLayout 钉住 .ico 容器的字节布局。
//
// 快捷方式的 IconLocation 指着这个文件（internal/shortcut）。容器写错了
// Windows **不会报错**，只是快捷方式显示成一张"白纸"——那正是这次要修的
// 现象，所以必须由测试挡住，不能靠肉眼看。
func TestICOContainerLayout(t *testing.T) {
	sizes := []int{16, 32, 48, 256}
	ico := ICOFileBytes(sizes...)

	u16 := func(off int) uint16 { return binary.LittleEndian.Uint16(ico[off:]) }
	u32 := func(off int) uint32 { return binary.LittleEndian.Uint32(ico[off:]) }

	// ICONDIR：reserved=0、type=1（图标）、count
	if got := u16(0); got != 0 {
		t.Errorf("reserved 应为 0，得到 %d", got)
	}
	if got := u16(2); got != 1 {
		t.Errorf("type 应为 1（图标），得到 %d", got)
	}
	if got := u16(4); got != uint16(len(sizes)) {
		t.Fatalf("图像数应为 %d，得到 %d", len(sizes), got)
	}

	headerLen := 6 + 16*len(sizes)
	if len(ico) <= headerLen {
		t.Fatalf("文件太短：%d 字节", len(ico))
	}

	// 逐项核对 ICONDIRENTRY，并确认每张图的 offset 落在正确位置、
	// 长度与 iconResourceBytes 的产出**完全一致**（同一份字节，不重新拼）。
	for i, sz := range sizes {
		base := 6 + 16*i
		w, h := int(ico[base]), int(ico[base+1])
		// 256 在 ICONDIRENTRY 里用 0 表示（字段只有一个字节）。
		if sz >= 256 {
			if w != 0 || h != 0 {
				t.Errorf("尺寸 %d 应写成 0，得到 %d/%d", sz, w, h)
			}
		} else if w != sz || h != sz {
			t.Errorf("尺寸应为 %d×%d，得到 %d×%d", sz, sz, w, h)
		}
		if got := u16(base + 4); got != 1 {
			t.Errorf("planes 应为 1，得到 %d", got)
		}
		if got := u16(base + 6); got != 32 {
			t.Errorf("bitCount 应为 32，得到 %d", got)
		}
		want := iconResourceBytes(sz)
		if got := int(u32(base + 8)); got != len(want) {
			t.Errorf("尺寸 %d 的 bytesInRes 应为 %d，得到 %d", sz, len(want), got)
		}
		off := int(u32(base + 12))
		if off < headerLen || off+len(want) > len(ico) {
			t.Fatalf("尺寸 %d 的 offset %d 越界（文件 %d 字节）", sz, off, len(ico))
		}
		if string(ico[off:off+len(want)]) != string(want) {
			t.Errorf("尺寸 %d 的图像数据与 iconResourceBytes 不一致", sz)
		}
		// BITMAPINFOHEADER 也要在：biSize=40、biHeight 两倍高度。
		if biSize := binary.LittleEndian.Uint32(ico[off:]); biSize != 40 {
			t.Errorf("尺寸 %d 的 biSize 应为 40，得到 %d", sz, biSize)
		}
		if biH := binary.LittleEndian.Uint32(ico[off+8:]); biH != uint32(sz*2) {
			t.Errorf("尺寸 %d 的 biHeight 应为 %d，得到 %d", sz, sz*2, biH)
		}
	}
}

// TestICOOpenableBySystem 让**系统自己**加载这个 .ico。
//
// 光对齐字节不够：`System.Drawing.Icon` 是最接近 Windows 资源管理器的
// 读法（它与 shell 走同一套 ico 解析），能加载出来才算真的能用。
// 非 Windows 上跳过。
func TestICOOpenableBySystem(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("只在 Windows 上验证系统能否加载")
	}
	if _, err := exec.LookPath("powershell"); err != nil {
		t.Skip("没有 powershell")
	}
	dir := t.TempDir()
	icoPath := filepath.Join(dir, "wbmux.ico")
	if err := os.WriteFile(icoPath, ICOFileBytes(16, 32, 48, 256), 0o644); err != nil {
		t.Fatalf("写 .ico 失败: %v", err)
	}
	// 输出编码钉成 UTF-8（与 shortcut 里同样的理由：OneDrive 重定向路径含中文）。
	script := "[Console]::OutputEncoding = [Text.Encoding]::UTF8; " +
		"Add-Type -AssemblyName System.Drawing; " +
		"$i = New-Object System.Drawing.Icon('" + icoPath + "'); " +
		"Write-Output (\"$($i.Width)x$($i.Height)\")"
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	text := string(out)
	if err != nil {
		t.Fatalf("系统加载 .ico 失败：%v\n%s", err, text)
	}
	t.Logf("系统读到的图标尺寸: %s", trimSpace(text))
	if trimSpace(text) == "" {
		t.Error("系统没有报错但也没给出尺寸")
	}
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
