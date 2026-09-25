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
	abs, err := filepath.Abs(exeFlag)
	if err != nil {
		return "", Install{}, fmt.Errorf("解析路径 %q 失败: %w", exeFlag, err)
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
