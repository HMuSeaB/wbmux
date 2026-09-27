#!/usr/bin/env python3
"""清理 go build 因被中断而泄漏的临时目录。

## 解决哪个具体问题

`go build` 会在 GOTMPDIR（未设置时就是系统 %TEMP%）下建一个
`go-buildNNNNNNNNN` 目录放编译中间产物，正常跑完会自己删掉。但构建
**被中断**时（Ctrl-C、超时被杀、编辑器或 CI 掐掉进程），清理代码没有
机会执行，目录就留下了——每次几十 MB。

这些残渣落在用户主目录的 %TEMP% 里，会被 Windows 的"存储感知"/磁盘清理
扫进**回收站**，于是在回收站里堆成成千上万个空目录，清理回收站时卡死。
实测 2026-09-27 那天：回收站里 5,503 项 / 297 MB，几乎全是这个。

## 配套的根治手段（比事后清更重要）

把 GOTMPDIR 指到固定位置，泄漏就集中在一处、且不再污染系统 %TEMP%：

    go env -w GOTMPDIR='C:\\Users\\<你>\\AppData\\Local\\go-tmp'

注意 `go env -w` 只写进 Go 自己的配置文件（%APPDATA%\\go\\env），
**不会**导出成进程环境变量——所以这个脚本要调 `go env` 去问，不能只看
os.environ（见 resolve_tmp_dir）。

## 为什么默认不清 %LOCALAPPDATA%\\go-build

那是**构建缓存**，不是垃圾。它让没改动的包不用重编，删了只会让下次
构建变慢。这个脚本只碰 `go-build` + 纯数字 的临时目录，并且要求目录里
确实有 Go 编译中间产物（`_pkg_.a` / `importcfg`）或是个空壳。

## 用法

    python tools/go-tmp/clean_gotmp.py            # 干跑，只列出
    python tools/go-tmp/clean_gotmp.py -f         # 真的删
    python tools/go-tmp/clean_gotmp.py -f --age 0 # 连刚生成的也删
"""

import argparse
import os
import re
import shutil
import subprocess
import sys
import time
from pathlib import Path

# go-build 后面跟纯数字，这才是 Go 自己建的临时目录。
# 加这个约束是为了不误伤别的同名目录（有的话）。
NAME_RE = re.compile(r'^go-build\d+$')

# 判定"这是 Go 编译中间产物"的标志文件
MARKERS = ('_pkg_.a', 'importcfg')


def resolve_tmp_dir():
    """找出 go build 到底把临时目录放哪了。

    这里有个坑：`go env -w GOTMPDIR=…` 只写进 Go 的配置文件
    （%APPDATA%\\go\\env），**不会**导出成进程环境变量。所以直接读
    os.environ 会读不到，脚本就会误判成"用系统 TEMP"，然后扫错地方、
    报告"没有需要清理的目录"——看着正常，实际什么都没清。

    顺序：显式环境变量 → 用 `go env` 问 Go 自己 → 系统 TEMP/TMP。
    """
    if os.environ.get('GOTMPDIR'):
        return os.environ['GOTMPDIR'], '环境变量 GOTMPDIR'
    try:
        out = subprocess.run(
            ['go', 'env', 'GOTMPDIR'],
            capture_output=True, text=True, timeout=20,
        )
        if out.returncode == 0 and out.stdout.strip():
            return out.stdout.strip(), 'go env GOTMPDIR（来自 Go 的配置文件）'
    except (OSError, subprocess.SubprocessError):
        pass
    for k in ('TEMP', 'TMP'):
        if os.environ.get(k):
            return os.environ[k], '系统 %s' % k
    return '', ''


def looks_like_go_scratch(d):
    """只删确实是 Go 编译残渣的目录。

    宁可漏删也不能误删：%TEMP% 里还可能有别的程序的中间产物。
    """
    for m in MARKERS:
        if (d / m).exists():
            return True
    try:
        entries = list(d.iterdir())
    except OSError:
        return False
    if not entries:
        return True  # 空壳：构建刚开始就被打断了
    for e in entries:
        if e.is_dir() and (e / '_pkg_.a').exists():
            return True
    return False


def dir_size(p):
    total = 0
    for root, _dirs, files in os.walk(p):
        for f in files:
            try:
                total += (Path(root) / f).stat().st_size
            except OSError:
                pass
    return total


def human_size(n):
    for unit, div in (('G', 1 << 30), ('M', 1 << 20), ('K', 1 << 10)):
        if n >= div:
            return '%.0f%s' % (n / div, unit)
    return '%dB' % n


def main():
    ap = argparse.ArgumentParser(description='清理 go build 泄漏的临时目录')
    ap.add_argument('-f', '--force', action='store_true',
                    help='真的删除（默认只列出）')
    ap.add_argument('--age', default=3600, type=int,
                    help='只清理超过这么多秒没动过的目录（默认 3600，避开正在跑的构建）')
    ap.add_argument('--dir', default='',
                    help='指定要扫的目录，跳过自动探测。'
                         '用于清理改了 GOTMPDIR 之前留在旧位置（通常是 %TEMP%）的残渣。')
    args = ap.parse_args()

    if args.dir:
        tmp, source = args.dir, '命令行指定'
    else:
        tmp, source = resolve_tmp_dir()
    if not tmp:
        print('找不到临时目录：GOTMPDIR / TEMP / TMP 都是空的', file=sys.stderr)
        return 1

    root = Path(tmp)
    if not root.is_dir():
        print('目录不存在：%s' % root, file=sys.stderr)
        return 1

    print('扫描 %s' % root)
    print('（来源：%s）\n' % source)

    cutoff = time.time() - args.age
    total = 0
    count = 0

    for entry in sorted(root.iterdir()):
        if not entry.is_dir() or not NAME_RE.match(entry.name):
            continue
        try:
            mtime = entry.stat().st_mtime
        except OSError:
            continue
        if mtime > cutoff:
            print('  跳过 %s（正在构建或太新）' % entry.name)
            continue
        if not looks_like_go_scratch(entry):
            print('  跳过 %s（不像 Go 构建产物）' % entry.name)
            continue

        size = dir_size(entry)
        total += size
        count += 1
        stamp = time.strftime('%m-%d %H:%M', time.localtime(mtime))
        if args.force:
            try:
                shutil.rmtree(entry)
            except OSError as e:
                print('  失败 %s: %s' % (entry.name, e))
                continue
            print('  已删 %s  %s' % (entry.name, human_size(size)))
        else:
            print('  可删 %s  %s  (%s)' % (entry.name, human_size(size), stamp))

    if not count:
        print('  没有需要清理的目录。')
        return 0

    if args.force:
        print('\n清理完成：%d 个目录，共 %s' % (count, human_size(total)))
    else:
        print('\n共 %d 个目录，%s。加 -f 才会真的删除。' % (count, human_size(total)))
    print('\n注意：%LOCALAPPDATA%\\go-build 是构建缓存，不在此列，也不该删——'
          '留着能让下次编译快很多。')
    return 0


if __name__ == '__main__':
    sys.exit(main())
