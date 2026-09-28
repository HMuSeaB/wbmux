package tray

import (
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// TestIconResourceLayout 钉住图标资源的字节布局。
//
// 这几处错了都不会报错，只会显示成一团或者干脆看不见，所以必须定死：
//   - BITMAPINFOHEADER 的 biHeight 是**两倍**高度（XOR + AND 叠在一起）
//   - XOR 位图自下而上
//   - 总长度 = 头 40 + XOR + 掩码（掩码每行 4 字节对齐）
func TestIconResourceLayout(t *testing.T) {
	for _, size := range []int{16, 32, 48} {
		bits := iconResourceBytes(size)

		xorSize := size * size * 4
		maskRow := ((size + 31) / 32) * 4
		maskSize := maskRow * size
		wantLen := 40 + xorSize + maskSize
		if len(bits) != wantLen {
			t.Fatalf("size=%d 长度应为 %d，得到 %d", size, wantLen, len(bits))
		}

		u32 := func(off int) uint32 { return binary.LittleEndian.Uint32(bits[off:]) }
		u16 := func(off int) uint16 { return binary.LittleEndian.Uint16(bits[off:]) }
		if got := u32(0); got != 40 {
			t.Errorf("size=%d biSize 应为 40，得到 %d", size, got)
		}
		if got := u32(4); got != uint32(size) {
			t.Errorf("size=%d biWidth 应为 %d，得到 %d", size, size, got)
		}
		if got := u32(8); got != uint32(size*2) {
			t.Errorf("size=%d biHeight 应为两倍高度的 %d，得到 %d", size, size*2, got)
		}
		// biPlanes / biBitCount 是 WORD。曾经把它们写成 4 字节，
		// 结果整个头长 44，图标直接废掉——测试就是为了挡这个。
		if got := u16(12); got != 1 {
			t.Errorf("size=%d biPlanes 应为 1，得到 %d", size, got)
		}
		if got := u16(14); got != 32 {
			t.Errorf("size=%d biBitCount 应为 32，得到 %d", size, got)
		}
		if got := u32(16); got != 0 {
			t.Errorf("size=%d biCompression 应为 BI_RGB(0)，得到 %d", size, got)
		}
		if got := u32(20); got != uint32(xorSize+maskSize) {
			t.Errorf("size=%d biSizeImage 应为 %d，得到 %d", size, xorSize+maskSize, got)
		}
	}
}

// TestIconPixelsAreSane 图像本身要"看得出是个东西"：四角透明、中心有颜色，
// 且图形（深色）确实压在底色（品牌色）上面。
func TestIconPixelsAreSane(t *testing.T) {
	size := iconSize
	bits := iconResourceBytes(size)
	pix := bits[40:]

	// at 取屏幕坐标 (x,y) 的 BGRA。XOR 位图自下而上，所以行号要翻。
	at := func(x, y int) (r, g, b, a byte) {
		off := ((size-1-y)*size + x) * 4
		return pix[off+2], pix[off+1], pix[off], pix[off+3]
	}

	// 圆角把四个角留空；alpha 必须真是 0（透明），而不是"画成黑色"。
	for _, c := range []struct{ x, y int }{{0, 0}, {size - 1, 0}, {0, size - 1}, {size - 1, size - 1}} {
		if _, _, _, a := at(c.x, c.y); a != 0 {
			t.Errorf("角 (%d,%d) 应为透明，alpha=%d", c.x, c.y, a)
		}
	}

	if _, _, _, a := at(size/2, size/2); a == 0 {
		t.Errorf("中心不该是透明的")
	}

	// 底板（左侧边，只有品牌色）与箭头（墨色）比亮度：后者应明显更暗。
	_, gBg, _, aBg := at(3, size/2)
	_, gInk, _, aInk := at(size/2, size*39/64)
	if aBg == 0 || aInk == 0 {
		t.Fatalf("取色点都不在图形上（底板 a=%d，箭头 a=%d），图形可能没画出来", aBg, aInk)
	}
	if gInk >= gBg {
		t.Errorf("箭头（墨色）应比底板暗：底板 G=%d，箭头 G=%d", gBg, gInk)
	}
}

// TestIconPreview 导出 PNG 供人眼看一眼。
//
// 默认跳过：它只是开发时的检查手段，不该在每次 go test 时往磁盘写图片。
//
//	WBMUX_ICON_PREVIEW=C:/tmp/icon.png go test ./internal/tray/ -run IconPreview
func TestIconPreview(t *testing.T) {
	out := os.Getenv("WBMUX_ICON_PREVIEW")
	if out == "" {
		t.Skip("需要 WBMUX_ICON_PREVIEW=<png 路径>")
	}
	// 这个用例跑在非 Windows 上也应该能用，所以直接从字节里读，不碰 HICON。

	const scale = 10 // 放大 10 倍，16px 的差别肉眼才看得清
	img := image.NewRGBA(image.Rect(0, 0, iconSize*scale, iconSize*scale))
	bits := iconResourceBytes(iconSize)
	pix := bits[40:]
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			off := ((iconSize-1-y)*iconSize + x) * 4
			// 棋盘底：用来确认透明区域真的是透明的。
			light := ((x/4)+(y/4))%2 == 0
			bg := color.RGBA{R: 0x3a, G: 0x3e, B: 0x46, A: 0xff}
			if light {
				bg = color.RGBA{R: 0x21, G: 0x23, B: 0x28, A: 0xff}
			}
			fg := color.RGBA{R: pix[off+2], G: pix[off+1], B: pix[off], A: pix[off+3]}
			blended := blend(bg, fg)
			for dy := 0; dy < scale; dy++ {
				for dx := 0; dx < scale; dx++ {
					img.Set(x*scale+dx, y*scale+dy, blended)
				}
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil && filepath.Dir(out) != "." {
		t.Fatalf("建目录失败：%v", err)
	}
	f, err := os.Create(out)
	if err != nil {
		t.Fatalf("创建 %s 失败：%v", out, err)
	}
	defer func() { _ = f.Close() }()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("编码 PNG 失败：%v", err)
	}
	t.Logf("已导出 %s", out)
}

// blend 把带 alpha 的前景叠在背景上（预览用）。
func blend(bg, fg color.RGBA) color.RGBA {
	a := float64(fg.A) / 255
	mix := func(b, f uint8) uint8 { return uint8(float64(b)*(1-a) + float64(f)*a + 0.5) }
	return color.RGBA{R: mix(bg.R, fg.R), G: mix(bg.G, fg.G), B: mix(bg.B, fg.B), A: 0xff}
}
