// Package autoupdate 关掉客户端的"启动时静默自动更新"。
//
// # 客户端有两条更新链路，读的配置来源不同
//
//  1. `early-silent-startup-update` —— **窗口出现之前**就跑，会直接静默装。
//     判定入口 `resolveEarlyStartupForceAutoEnabled()`，取值优先级：
//
//     userSetting（app/app-config.json）      ← 我们要写的层
//     → remoteFeature（远程实时下发）
//     → cachedSystemDefault（app-config.json）
//     → productBase（**安装目录** cli/product.json）
//     → hardcoded true
//
//  2. `Startup force auto-update` —— 窗口起来之后才跑，读经 daemon-state 的配置。
//
// # 为什么写 app-config.json，而不是 settings.json
//
// 2026-10-04 那次把 `startupForceAutoUpdate` 写进了 `settings.json`——
// **写错文件了**。early-silent 读的是 `app/app-config.json`，`settings.json`
// 里那个键根本没人看，于是那条链路一直是 `product-base` 的 true：
//
//	[early-silent-startup-update] enabled=true source=product-base
//
// 这就是"防更新没用、'正在安装更新'窗口照旧冒出来"的真正原因——
// 不是值不对，是**放错了文件**。
//
// # 为什么不去改安装目录的 product.json
//
// 那层（`productBase`）优先级最低，改它要把手伸进安装目录：需要管理员权限
// （装的在 Program Files 时）、且客户端一升级就被覆盖。而 app-config.json
// 在**用户数据目录**里（`~/.workbuddy/app/`），既不用提权也不会被更新冲掉，
// 正是想要的"改一次长期有效"。
package autoupdate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Key 是 app-config.json 里那个开关的名字。
const Key = "startupForceAutoUpdate"

// Ensure 确保 `app-config.json` 里 `startupForceAutoUpdate` 为 false。
//
// 返回是否真的动了文件。**已经是对的值就一个字节都不写**——这类"顺手改一下
// 别人的设置文件"的操作，能不动就不动：每写一次就多一次写坏别人文件的机会。
func Ensure(appConfigPath string) (bool, error) {
	doc, existed, err := load(appConfigPath)
	if err != nil {
		return false, err
	}
	if v, ok := doc[Key]; ok {
		var cur bool
		if json.Unmarshal(v, &cur) == nil && !cur {
			return false, nil // 已经是关着的，收工
		}
	}
	doc[Key] = json.RawMessage("false")
	return save(appConfigPath, doc, existed)
}

// Release 把 `startupForceAutoUpdate` 这个键**删掉**，交回客户端自己决定。
//
// 为什么不写 `true`：那是替用户做了"允许自动更新"这个决定。用户想表达的
// 只是"这事我不管了"——删掉才等价于"没管过"，客户端会按它自己的优先级链
// （远程下发 / 缓存默认 / 安装目录）去决定，将来官方改策略也能跟着走。
//
// 键本来就不在时返回 false（没东西可改）。
func Release(appConfigPath string) (bool, error) {
	doc, existed, err := load(appConfigPath)
	if err != nil {
		return false, err
	}
	if _, ok := doc[Key]; !ok {
		return false, nil
	}
	delete(doc, Key)
	// 删完键之后如果文件空了、而原本就不存在，就没有写的必要；
	// 但只要文件存在过，就该把"删掉键"这个结果落盘。
	return save(appConfigPath, doc, existed)
}

// save 把一个文档原子地写到 path。existed 表示原文件是否已存在
// （决定要不要留备份）。
func save(path string, doc map[string]json.RawMessage, existed bool) (bool, error) {
	out, err := marshal(doc)
	if err != nil {
		return false, err
	}

	// 写前备份。只备份"本来就存在"的文件——文件不存在时没有可备份的内容，
	// 凭空造一个 .bak 反而让人以为原来有配置。
	if existed {
		if raw, err := os.ReadFile(path); err == nil {
			_ = os.WriteFile(path+".wbmux-bak", raw, 0o644)
		}
	}

	// 原子写：先写同目录临时文件再 rename。直接覆盖的话，写到一半崩溃会留下
	// 半截 JSON，客户端读不动就可能把整份应用配置重置掉。
	// （本项目 config.Save / custommodels.Sync 都是这个模式，这里跟上。）
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".wbmux-tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}

// load 读 app-config.json。第二个返回值表示文件是否存在。
//
// 读不出来（不存在、空文件、乱码）时返回**空文档**而不是报错：这个文件的
// 职责只有一个键，别的键我们一律原样保留；从空开始不会丢任何东西。
// 但"权限/IO 错误"要如实报上去，别猜。
func load(path string) (map[string]json.RawMessage, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, false, nil
		}
		return nil, false, err
	}
	doc := map[string]json.RawMessage{}
	trimmed := bytes.TrimSpace(raw)
	// 空文件与 `null` 都当"没有内容"：json.Unmarshal 会接受它们并留下空 map，
	// 但显式判断更清楚——这里不需要为它们走一遍解析。
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return doc, true, nil
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		// 内容坏了：留档一份供事后查看，然后从空文档重建。
		// 不这么做的话，客户端读不动这份文件时会自己重置，用户的其他设置
		// （比如 locale）就跟着没了。
		_ = os.WriteFile(path+".wbmux-corrupt", raw, 0o644)
		return map[string]json.RawMessage{}, true, nil
	}
	return doc, true, nil
}

// marshal 序列化。用 json.RawMessage 承载每个值，**未知键原样保留**。
//
// 代价是键顺序会被排成字典序（Go 的 map 没有顺序）。对这个文件无所谓：
// 客户端按对象读，不看顺序。真正要守的是"别丢键"，那一点由 RawMessage 保证。
func marshal(doc map[string]json.RawMessage) ([]byte, error) {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化 app-config.json 失败：%w", err)
	}
	return append(out, '\n'), nil
}

// Enabled 读当前值，报告"启动静默更新是否开着"。
//
// 只用于界面展示与体检：判断的依据是**文件里有没有那个键**，
// 而不是替客户端跑一遍它那套优先级链（那需要读安装目录，且会随版本变）。
// 返回 ok=false 表示键没设过，此时客户端会落到安装目录的 productBase。
func Enabled(appConfigPath string) (enabled bool, ok bool) {
	doc, _, err := load(appConfigPath)
	if err != nil {
		return false, false
	}
	v, exists := doc[Key]
	if !exists {
		return false, false
	}
	var cur bool
	if json.Unmarshal(v, &cur) != nil {
		return false, false
	}
	return cur, true
}
