package variant

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Resolve 定位宿主安装。
//
// 优先级：显式路径 > 显式档位 > 自动探测。自动探测时国内版优先，
// 它是更常见的默认安装。
//
// 返回的错误已经带上可操作的建议，可以直接展示给用户。
func Resolve(p *Probe, hostFlag, exeFlag string) (ID, Install, error) {
	if exeFlag != "" {
		return resolveFromExe(p, hostFlag, exeFlag)
	}

	if hostFlag != "" {
		id, err := Parse(hostFlag)
		if err != nil {
			return "", Install{}, err
		}
		inst := p.Detect(id, "")
		if !inst.Found {
			return id, inst, NotFoundError(id, inst)
		}
		return id, inst, nil
	}

	var problems []string
	for _, id := range []ID{CN, Intl} {
		inst := p.Detect(id, "")
		if inst.Found {
			return id, inst, nil
		}
		problems = append(problems, string(id)+": "+strings.Join(inst.Problems, "; "))
	}
	return "", Install{}, fmt.Errorf(
		"未能定位任何客户端安装（%s）\n提示：用 `wbmux config set --exe <主程序绝对路径>` 显式指定",
		strings.Join(problems, "；"))
}

// resolveFromExe 处理用户显式给出主程序路径的情况。
//
// 档位能推断就推断，推断不出来就把两套都试一遍——
// 因为路径本身可能完全不含档位特征（比如被重命名过的目录）。
func resolveFromExe(p *Probe, hostFlag, exeFlag string) (ID, Install, error) {
	abs, err := normalizePath(exeFlag)
	if err != nil {
		return "", Install{}, err
	}

	var candidates []ID
	switch {
	case hostFlag != "":
		id, err := Parse(hostFlag)
		if err != nil {
			return "", Install{}, err
		}
		candidates = []ID{id}
	default:
		if g := GuessFromPath(abs); g != "" {
			candidates = []ID{g}
		} else {
			candidates = []ID{CN, Intl}
		}
	}

	var (
		last        Install
		firstDetail string
		sameDetail  = true
	)
	for i, id := range candidates {
		inst := p.Detect(id, abs)
		if inst.Found {
			return id, inst, nil
		}
		detail := strings.Join(inst.Problems, "; ")
		switch {
		case i == 0:
			firstDetail = detail
		case detail != firstDetail:
			sameDetail = false
		}
		last = inst
	}

	// 各候选给出的原因一致，说明问题出在路径本身而不是档位判断，
	// 直接透出原因，免得拼出"指定的主程序不可用：指定的主程序不存在：..."。
	if len(candidates) == 1 {
		return candidates[0], last, errors.New(firstDetail)
	}
	if sameDetail {
		return "", last, errors.New(firstDetail)
	}
	return "", last, fmt.Errorf("指定的主程序不可用: %s", strings.Join(last.Problems, "; "))
}

// NotFoundError 把"某个档位没找到"包装成可直接展示的错误。
func NotFoundError(id ID, inst Install) error {
	b, err := Get(id)
	if err != nil {
		return err
	}
	detail := strings.Join(inst.Problems, "; ")
	if detail == "" {
		detail = "未在常见位置找到"
	}
	return fmt.Errorf("未找到%s的安装：%s\n提示：用 `wbmux config set --exe <主程序绝对路径>` 显式指定",
		b.DisplayName, detail)
}

// normalizePath 把用户输入的路径规整为绝对路径。
//
// 关键点：只有相对路径才拼工作目录。filepath.Abs 按**当前平台**判断是否
// 绝对，在 Linux/macOS 上会把 `D:\App\App.exe` 当成相对路径，拼成
// `/cwd/D:\App\App.exe` ——路径被悄悄改坏，用户只会看到"指定的主程序不存在"。
//
// 同理，属于**别的平台风格**的绝对路径也不交给 filepath.Clean：
// Clean 按本平台的分隔符工作，在 Windows 上会把 `/opt/app` 改形成
// `\opt\app`，反而破坏了它。原样返回是最老实的做法。
func normalizePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("路径为空")
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	if IsAbsolutePath(p) {
		return p, nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("解析路径 %q 失败: %w", p, err)
	}
	return filepath.Clean(abs), nil
}

// IsAbsolutePath 判断路径是否为绝对路径，同时认 Windows 与类 Unix 两种写法。
//
// 不能只用 filepath.IsAbs：它按当前平台判断。实测两个方向的坑：
//   - 在 Linux/macOS 上，`D:\App\App.exe` 被判为相对路径；
//   - 在 Windows 上，`/opt/app` 被判为相对路径。
//
// 两种情况下 filepath.Abs 都会把路径拼到工作目录后面，把用户给的路径改坏。
// 因此这里额外认盘符形式（`D:\`、`D:/`）、UNC 形式（`\\server\share`）
// 与前导斜杠。
func IsAbsolutePath(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	// 盘符形式。注意 `D:` 单独出现不是绝对路径，必须是 `D:\` 或 `D:/`。
	if len(p) >= 3 && isDriveLetter(p[0]) && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		return true
	}
	// UNC 形式与前导斜杠。
	return strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "/")
}

func isDriveLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
