#!/usr/bin/env python3
"""确认一批 Win32 API 各自属于哪个 DLL。

用途
----
wbmux 用 syscall 直调系统 API，而 Go 的 `syscall.LazyProc.Call` 在
**找不到函数时会直接 panic**，不是返回错误。所以「把 API 挂在错的 DLL 上」
是一类会悄悄埋下、又在特定分支才炸的错误。

最严重的一次：`ShowWindow` 被写成 kernel32 的 proc（它实际在 user32），
结果双击启动时每次都 panic 在"收起控制台"那一步，而 panic 信息正好被写进
那个刚被隐藏的控制台——用户看到的是"双击了，什么都没发生"。

排查那次用的就是这类脚本；项目里现在有 permanent 的回归测试
（`TestProcsResolve`），但独立跑一次仍然适合：
- 还没进代码就想先确认某个 API 在哪
- 怀疑某个历史版本有问题，又不想改仓库

用法
----
    python proc_dll_probe.py                  # 跑内置的默认清单
    python proc_dll_probe.py kernel32 ShowWindow user32 ShowWindow
      # 参数按「DLL 名 / 函数名」成对给出
"""
import ctypes
import sys

# 默认清单：wbmux 目前用到的全部 Win32 proc，按 DLL 分组。
DEFAULT = {
    "kernel32": ["SetConsoleOutputCP", "SetConsoleCP", "GetConsoleMode",
                 "SetConsoleMode", "GetConsoleProcessList", "GetConsoleWindow",
                 "GetModuleHandleW"],
    "user32": ["ShowWindow", "RegisterClassExW", "CreateWindowExW", "DefWindowProcW",
               "GetMessageW", "TranslateMessage", "DispatchMessageW", "PostQuitMessage",
               "CreatePopupMenu", "AppendMenuW", "TrackPopupMenu", "DestroyMenu",
               "SetForegroundWindow", "LoadIconW", "GetCursorPos", "PostMessageW",
               "DestroyWindow"],
    "shell32": ["Shell_NotifyIconW"],
    "advapi32": ["RegOpenKeyExW", "RegEnumKeyExW", "RegQueryValueExW", "RegCloseKey"],
}


def probe(dll: str, func: str) -> bool:
    """在指定 DLL 里找这个函数；找到返回 True。"""
    try:
        lib = ctypes.WinDLL(dll)
    except OSError as e:
        print("  [!] 打不开 %s：%s" % (dll, e))
        return False
    try:
        getattr(lib, func)
        return True
    except AttributeError:
        return False


def main(argv):
    if len(argv) > 1 and len(argv) % 2 == 1:
        pairs = list(zip(argv[1::2], argv[2::2]))
        plan = {}
        for dll, func in pairs:
            plan.setdefault(dll, []).append(func)
    else:
        plan = DEFAULT

    bad = 0
    for dll, funcs in plan.items():
        print("=== %s ===" % dll)
        for f in funcs:
            ok = probe(dll, f)
            print("  %-24s %s" % (f, "OK" if ok else "找不到 ← 挂错 DLL 或名字拼错"))
            if not ok:
                bad += 1
        print()
    if bad:
        print("有 %d 项解析失败" % bad)
        return 1
    print("全部解析成功")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
