package main

import (
	"fmt"
	"strings"
)

// flags 是一个极简命令行解析器。
//
// 不直接用标准库 flag 包：flag 在遇到第一个位置参数后就停止解析，
// 而本工具希望 `wbmux run intl --dry-run` 与 `wbmux run --dry-run intl` 等价。
//
// 支持的形式：
//
//	--name            布尔开关
//	--name=value      带值
//	--name value      带值
//	-n                短名（通过 Alias 注册）
//	--                其后全部视为位置参数
type flags struct {
	boolNames  map[string]*bool
	strNames   map[string]*string
	listNames  map[string]*[]string
	shortNames map[string]string

	// Args 是解析完成后剩下的位置参数。
	Args []string
}

func newFlags() *flags {
	return &flags{
		boolNames:  map[string]*bool{},
		strNames:   map[string]*string{},
		listNames:  map[string]*[]string{},
		shortNames: map[string]string{},
	}
}

// Bool 注册一个布尔开关，返回可读写的指针。
func (f *flags) Bool(name string, def bool) *bool {
	v := new(bool)
	*v = def
	f.boolNames[name] = v
	return v
}

// String 注册一个字符串选项，返回可读写的指针。
func (f *flags) String(name, def string) *string {
	v := new(string)
	*v = def
	f.strNames[name] = v
	return v
}

// List 注册一个可重复出现的选项，每次出现追加一个值。
func (f *flags) List(name string) *[]string {
	v := new([]string)
	f.listNames[name] = v
	return v
}

// Alias 把短名指向长名。
func (f *flags) Alias(short, long string) {
	f.shortNames[short] = long
}

// Parse 解析 args，错误信息可直接展示给用户。
func (f *flags) Parse(args []string) error {
	i := 0
	for i < len(args) {
		a := args[i]
		i++

		if a == "--" {
			f.Args = append(f.Args, args[i:]...)
			return nil
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			f.Args = append(f.Args, a)
			continue
		}

		name := strings.TrimLeft(a, "-")
		value := ""
		hasValue := false
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name, value, hasValue = name[:eq], name[eq+1:], true
		}
		if long, ok := f.shortNames[name]; ok {
			name = long
		}

		// 布尔开关：--flag 或 --flag=false
		if p, ok := f.boolNames[name]; ok {
			if hasValue {
				b, err := parseBool(value)
				if err != nil {
					return fmt.Errorf("--%s: %w", name, err)
				}
				*p = b
			} else {
				*p = true
			}
			continue
		}

		// 带值选项：值可能跟在下一个参数上
		if p, ok := f.strNames[name]; ok {
			v, next, err := takeValue(name, value, hasValue, args, i)
			if err != nil {
				return err
			}
			i = next
			*p = v
			continue
		}

		if p, ok := f.listNames[name]; ok {
			v, next, err := takeValue(name, value, hasValue, args, i)
			if err != nil {
				return err
			}
			i = next
			*p = append(*p, v)
			continue
		}

		return fmt.Errorf("未知选项 %s（试试 `wbmux help`）", a)
	}
	return nil
}

func takeValue(name, inline string, hasInline bool, args []string, i int) (string, int, error) {
	if hasInline {
		return inline, i, nil
	}
	if i >= len(args) {
		return "", i, fmt.Errorf("--%s 需要一个值", name)
	}
	return args[i], i + 1, nil
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("无法识别的布尔值 %q", s)
}
