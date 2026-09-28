package tray

import "math"

// 托盘图标是运行时**画出来**的，不嵌资源文件。
//
// # 为什么自己画
//
// 原先用的是系统通用应用图标（`LoadIconW(NULL, IDI_APPLICATION)`）——一个
// "窗口"形状的灰框。后果是通知区域里出现多个通用图标时根本分不清哪个是
// wbmux（用户 2026-09-27 的原话："忽然有两个这个了，而且这上面的显示界面
// 没用啊"）。
//
// 自己做还避开了另一条路：给二进制嵌 .ico 资源。那要动构建脚本与各平台的
// 资源编译器，和"单文件、零依赖、一个 runner 出全部平台"的取向不搭。
// 运行时画一张 32×32 的图，代价是几十行数学。
//
// # 产出格式
//
// `CreateIconFromResourceEx` 认的是**单个图标图像**的资源格式，不是整个
// .ico 文件：
//
//	BITMAPINFOHEADER（40 字节，biHeight 写 2×size）   // XOR 与 AND 叠在一起
//	XOR 位图：size×size 的 BGRA，**自下而上**逐行
//	AND 掩码：1bpp，每行按 4 字节对齐；为 1 的位表示透明
//
// 这几处（高度翻倍、行序自下而上、掩码行对齐）都极易写错，且错了不一定
// 报错，只会显示成一团。所以这里是纯函数，字节布局由 icon_test.go 钉住。

// iconSize 是图标的边长。32 是托盘图标的常用取值：系统缩到 16 也还认得出。
const iconSize = 32

// brandRGB / inkRGB 是配色，取自界面的品牌色与最深的中性色
// （见 internal/webui/assets/index.html 的 --brand / --g0）。
//
// 底色用品牌色而不是透明：通知区域的底色在浅色/深色主题下不同，
// 纯前景色的图标总有一种主题会糊掉；实底方块在两种主题下都清楚。
var (
	brandRGB = [3]float64{0x63, 0xb3, 0xc4} // #63b3c4
	inkRGB   = [3]float64{0x0b, 0x0c, 0x0e} // #0b0c0e
)

// iconResourceBytes 生成 size×size 的图标资源字节。
func iconResourceBytes(size int) []byte {
	pix := make([]byte, size*size*4)
	drawIcon(pix, size)

	maskRow := ((size + 31) / 32) * 4 // 1bpp 行按 4 字节对齐
	mask := make([]byte, maskRow*size)

	header := bitmapInfoHeader(size, len(pix)+len(mask))

	out := make([]byte, 0, len(header)+len(pix)+len(mask))
	out = append(out, header...)
	out = append(out, pix...)
	out = append(out, mask...)
	return out
}

// bitmapInfoHeader 拼 BITMAPINFOHEADER。
//
// biHeight 必须是**两倍**高度：Win32 把 XOR 位图与 AND 掩码当成上下两张
// 拼在一起的一张图。写成单倍高度的话，系统会按一半的高度去读，图标就废了。
// biPlanes / biBitCount 是 WORD（2 字节），别顺手写成 4 字节。
func bitmapInfoHeader(size, bitsSize int) []byte {
	le32 := func(v uint32) []byte {
		return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
	}
	le16 := func(v uint16) []byte { return []byte{byte(v), byte(v >> 8)} }

	h := make([]byte, 0, 40)
	h = append(h, le32(40)...)               // biSize
	h = append(h, le32(uint32(size))...)     // biWidth
	h = append(h, le32(uint32(size*2))...)   // biHeight（两倍，见上）
	h = append(h, le16(1)...)                // biPlanes
	h = append(h, le16(32)...)               // biBitCount
	h = append(h, le32(0)...)                // biCompression = BI_RGB
	h = append(h, le32(uint32(bitsSize))...) // biSizeImage
	h = append(h, le32(0)...)                // biXPelsPerMeter
	h = append(h, le32(0)...)                // biYPelsPerMeter
	h = append(h, le32(0)...)                // biClrUsed
	h = append(h, le32(0)...)                // biClrImportant
	return h
}

// drawIcon 把图标画进 BGRA 缓冲（左上角为原点，一行 size*4 字节）。
//
// 自下而上翻转在这里不做：翻转是"资源格式"的要求，由 iconResourceBytes
// 之前的写入顺序决定——所以这里先按自然行序画，再整体翻。
func drawIcon(pix []byte, size int) {
	s := float64(size)
	// 几何按 32 的坐标系定义，再按 size 缩放：这样换尺寸不用重算魔数。
	scale := s / 32

	// 底板：圆角方块，内缩一点，避免贴边被裁。
	bg := roundRect{
		x0: 1 * scale, y0: 1 * scale,
		x1: 31 * scale, y1: 31 * scale,
		r: 7 * scale,
	}
	// 图形：一对反向箭头（⇄），呼应"一份安装连两套后端"。
	inkHalf := 1.6 * scale
	upper := arrow{
		barX0: 7 * scale, barX1: 22 * scale, cy: 12.5 * scale,
		headX: 26.5 * scale, headDir: +1, headHalf: 4.6 * scale, barHalf: inkHalf,
	}
	lower := arrow{
		barX0: 10 * scale, barX1: 25 * scale, cy: 19.5 * scale,
		headX: 5.5 * scale, headDir: -1, headHalf: 4.6 * scale, barHalf: inkHalf,
	}

	// 3×3 超采样：纯二值化的圆角与斜边在 16px 下会锯齿很重。
	const ss = 3
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var bgCov, inkCov float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := float64(px) + (float64(sx)+0.5)/ss
					y := float64(py) + (float64(sy)+0.5)/ss
					if !bg.contains(x, y) {
						continue
					}
					bgCov++
					if upper.contains(x, y) || lower.contains(x, y) {
						inkCov++
					}
				}
			}
			total := float64(ss * ss)
			if bgCov == 0 {
				continue // 留空：alpha 0 就是透明
			}
			alpha := bgCov / total
			inkFrac := inkCov / bgCov
			col := [3]float64{}
			for i := 0; i < 3; i++ {
				col[i] = brandRGB[i]*(1-inkFrac) + inkRGB[i]*inkFrac
			}
			// BGRA 顺序，且行序要自下而上：这里直接写翻转后的行号。
			row := size - 1 - py
			off := (row*size + px) * 4
			pix[off+0] = clamp8(col[2])
			pix[off+1] = clamp8(col[1])
			pix[off+2] = clamp8(col[0])
			pix[off+3] = clamp8(alpha * 255)
		}
	}
}

func clamp8(v float64) byte {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return byte(v + 0.5)
}

// roundRect 是圆角矩形（按"点到矩形的距离"判定，兼顾直边与四个圆角）。
type roundRect struct {
	x0, y0, x1, y1, r float64
}

func (rr roundRect) contains(x, y float64) bool {
	if x < rr.x0 || x > rr.x1 || y < rr.y0 || y > rr.y1 {
		return false
	}
	// 落在四个角的正交区域里时，额外检查是否在圆内。
	cx := math.Min(math.Max(x, rr.x0+rr.r), rr.x1-rr.r)
	cy := math.Min(math.Max(y, rr.y0+rr.r), rr.y1-rr.r)
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= rr.r*rr.r
}

// arrow 是一支水平箭头：一条横杠 + 一个三角形头。
type arrow struct {
	barX0, barX1 float64 // 横杠的 x 范围
	cy           float64 // 轴线高度
	barHalf      float64 // 横杠半厚
	headX        float64 // 箭尖的 x
	headDir      float64 // +1 指向右，-1 指向左
	headHalf     float64 // 箭头底部的半高
}

func (a arrow) contains(x, y float64) bool {
	dy := math.Abs(y - a.cy)
	if x >= a.barX0 && x <= a.barX1 && dy <= a.barHalf {
		return true
	}
	// 三角从**横杠靠近箭尖的那一端**（base）向箭尖线性收窄。
	// 左向箭头要用 barX0 当基准——第一版错用了 barX1，结果画出一个横跨
	// 整个图形的巨大楔形（预览图一眼看出）。
	base := a.barX1
	if a.headDir < 0 {
		base = a.barX0
	}
	span := math.Abs(a.headX - base)
	if span <= 0 {
		return false
	}
	t := (a.headX - x) * a.headDir // 0 在箭尖，span 在底边
	if t < 0 || t > span {
		return false
	}
	return dy <= a.headHalf*(t/span)
}

// ICOFileBytes 把自画图标组装成一个**完整的 .ico 文件**，含多个尺寸。
//
// 用途：桌面快捷方式的 IconLocation 指向它（internal/shortcut）。托盘
// 图标运行时画（见上），桌面快捷方式的图标总不能是"白纸"——exe 本体
// 不嵌资源（同样的取舍，见文件头的说明），所以运行时把画好的图落成
// .ico 文件给快捷方式指。
//
// .ico 容器格式：6 字节 ICONDIR（保留字 0 / 类型 1 / 图像数）+ 每图
// 16 字节 ICONDIRENTRY（宽高各 1 字节，256 写 0）+ 依序排布的图像数据。
// 图像数据正是 iconResourceBytes 的产出（BITMAPINFOHEADER + XOR + AND），
// 一字不用改。
func ICOFileBytes(sizes ...int) []byte {
	type img struct {
		w, h, size int
		data       []byte
	}
	var imgs []img
	var total int
	for _, sz := range sizes {
		data := iconResourceBytes(sz)
		imgs = append(imgs, img{sz, sz, len(data), data})
		total += len(data)
	}

	le16 := func(v uint16) []byte { return []byte{byte(v), byte(v >> 8)} }
	le32 := func(v uint32) []byte {
		return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
	}

	out := make([]byte, 0, 6+16*len(imgs)+total)
	// ICONDIR：reserved / type=1（图标）/ 图像数。
	// 注意 le16 返回的是切片：不能与单独的 byte 混在同一个 append 里，
	// 得逐个 append 各自带 "..."（或像下面这样分开写）。
	out = append(out, le16(0)...)
	out = append(out, le16(1)...)
	out = append(out, le16(uint16(len(imgs)))...)
	offset := 6 + 16*len(imgs)
	for _, im := range imgs {
		w, h := byte(im.w), byte(im.h)
		if im.w >= 256 {
			w = 0
		}
		if im.h >= 256 {
			h = 0
		}
		out = append(out, w, h, 0, 0)
		out = append(out, le16(1)...)               // planes
		out = append(out, le16(32)...)              // bitCount
		out = append(out, le32(uint32(im.size))...) // bytesInRes
		out = append(out, le32(uint32(offset))...)
		offset += im.size
	}
	for _, im := range imgs {
		out = append(out, im.data...)
	}
	return out
}
