package migrate

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// CompressCwd 把工作目录路径压缩成 projects/ 下的目录名。
//
// 规则是从本机现存数据反推出来的：国内版 9 个、国际版 2 个目录，
// 全部 11 个都能由会话自带的 cwd 字段精确算回，无一例外。
//
// 做法是先把 ':' '/' '\' 一律换成 '-'，把连续 '-' 折叠成一个，
// 再把首字符小写（盘符）。
//
// 客户端的注释把这个变换称作"有损压缩"（compressedCwd），确实有损——
// 路径里本来就含 "--" 的会被吃掉一段。这可以接受：目录名只是个索引键，
// 而会话正文里每一行都带着权威的 cwd 字段，界面按后者显示。
func CompressCwd(cwd string) string {
	var b strings.Builder
	b.Grow(len(cwd) + 8)

	dash := false
	for i := 0; i < len(cwd); i++ {
		c := cwd[i]
		// 只比较 ASCII 分隔符：UTF-8 续字节都 >= 0x80，
		// 所以逐字节扫描不会误伤中文路径。
		if c == ':' || c == '/' || c == '\\' {
			if !dash {
				b.WriteByte('-')
				dash = true
			}
			continue
		}
		b.WriteByte(c)
		dash = false
	}

	s := b.String()
	if s == "" {
		return ""
	}

	// 只降首字符：盘符大小写在两侧不一致（DB 里是 "C:"，目录名是 "c-"），
	// 但路径其余部分的大小写是用户自己定的，动它会算错。
	first, size := utf8.DecodeRuneInString(s)
	if first == utf8.RuneError && size <= 1 {
		return s
	}
	return string(unicode.ToLower(first)) + s[size:]
}
