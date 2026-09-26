#!/usr/bin/env python3
"""检查一个 Mach-O 二进制有没有 LC_UUID 这个 load command。

用途
----
macOS 26 的 dyld 要求二进制带 LC_UUID；缺了就是：

    dyld: missing LC_UUID load command
    signal: abort trap

wbmux 的 CI 曾经恰好卡在这上面：Go 1.22 的链接器不给 darwin/arm64
写 LC_UUID，而 Go 1.24 会写。现象只在 macOS 上出现，且**被构建缓存长期
遮住**——缓存命中时测试二进制根本不在 runner 上真正执行。

有了这个脚本，可以在 Windows 上直接判定 macOS 侧的产物，不用等 CI。

用法
----
    python macos_uuid.py <二进制> [<二进制> ...]

退出码：全部带 LC_UUID 返回 0，否则 1。
"""
import struct
import sys

LC_UUID = 0x1B


def check(path):
    """返回 (uuid 列表, 说明)。uuid 为空即缺失。"""
    with open(path, "rb") as f:
        d = f.read()
    magic = struct.unpack("<I", d[:4])[0]
    if magic not in (0xFEEDFACF, 0xFEEDFACE):
        return [], "不是 Mach-O（magic=%#x）" % magic
    ncmds = struct.unpack("<I", d[16:20])[0]
    off = 32
    uuids = []
    for _ in range(ncmds):
        cmd, cmdsize = struct.unpack("<II", d[off:off + 8])
        if cmd == LC_UUID:
            uuids.append(d[off + 8:off + 24].hex())
        off += cmdsize
    return uuids, "ncmds=%d" % ncmds


def main(argv):
    if len(argv) < 2:
        print(__doc__)
        return 2
    bad = 0
    for p in argv[1:]:
        u, note = check(p)
        tag = ("有 LC_UUID  " + ", ".join(u)) if u else "【缺少 LC_UUID】"
        print("  %-46s %s  (%s)" % (p.split("/")[-1], tag, note))
        if not u:
            bad += 1
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
