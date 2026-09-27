#!/usr/bin/env python3
"""诊断并清理"是谁在往回收站塞垃圾"。

## 解决哪个具体问题

用户反馈回收站被灌满、清理时卡死。真正的问题**不是体积，是条目数**：
这些条目每个只有 4~68 字节，但堆到几千个之后，回收站 UI 加载缩略图和
属性就要逐个查元数据，直接卡死。

## 实测到的来源（2026-09-27，逐条对过原始路径）

读回收站的 $I 元数据文件（含"原路径"字段，UTF-16LE）拿到确证：

| 原路径 | 谁产生的 |
|---|---|
| `%TEMP%\\__PSScriptPolicyTest_*.ps1`  | **PowerShell 每次启动都建**（探测 AppLocker 策略），内容固定是
| `%TEMP%\\__PSScriptPolicyTest_*.psm1` | `# PowerShell test file to determine AppLocker lockdown mode` |
| `%TEMP%\\<8位随机>` | 各类工具（bash 工具每次调用建一个） |
| `%TEMP%\\go-buildNNNNNNN` | go build 被中断（已用 GOTMPDIR 修掉，见根目录说明） |

关键结论：**这是"每次调用工具就产生几个文件"的模式**，不是一次性泄漏。
只要在对话里干活，回收站就会持续增长。

## 这个脚本做什么

1. **`report`（默认）**：读回收站元数据，按"原路径"聚合，告诉你到底是谁
   在塞、各有多少条。不需要管理员权限。
2. **`clean`**：把回收站里**符合已知识别特征**的噪声条目真正删掉
   （不是恢复到原位置，是彻底清掉）。默认只处理已知的工具噪声，
   用户自己删的文件（如安装包）不动。

## 用法

    python tools/recycle-noise/recycle_noise.py                 # 报告
    python tools/recycle-noise/recycle_noise.py clean           # 清噪声
    python tools/recycle-noise/recycle_noise.py clean --all     # 清空回收站

## 为什么不直接"清空回收站"

用户自己删的文件（比如那个 253 MB 的安装包）可能还想恢复。所以默认只清
**匹配已知噪声特征**的条目，其余留给用户自己决定。
"""

import argparse
import glob
import os
import re
import struct
import sys
import time
from collections import Counter

# ---------------------------------------------------------------- 回收站定位

def find_recycle_dirs():
    """找出所有盘的回收站里当前用户的 SID 目录。"""
    out = []
    for drive in 'CDEFGH':
        base = '%s:\\$Recycle.Bin' % drive
        if not os.path.isdir(base):
            continue
        try:
            for sid in os.listdir(base):
                if not sid.startswith('S-1-'):
                    continue
                d = os.path.join(base, sid)
                if os.path.isdir(d):
                    out.append(d)
        except OSError:
            pass
    return out


def read_meta(path):
    """从 $I 文件读出被删项的原始路径与大小。

    格式（Windows Vista+，版本 2）——实测出来的布局，网上有些说法是错的：

        0..8     版本号（2）
        8..16    原始文件大小
        16..24   删除时间（FILETIME）
        24..28   路径长度（字符数，不含结尾 NUL）
        28..     路径，UTF-16LE

    注意偏移 24 那 4 个字节**是路径长度，不是路径的一部分**。早期版本
    直接写"路径从 24 开始"，结果每个路径前面都多出一个怪字符（那其实是
    长度值的第一个字节）。用长度字段来切，既准确又能顺带校验。
    """
    with open(path, 'rb') as f:
        d = f.read()
    if len(d) < 28:
        return None
    ver = struct.unpack('<Q', d[0:8])[0]
    size = struct.unpack('<Q', d[8:16])[0]
    nchars = struct.unpack('<I', d[24:28])[0]

    raw = d[28:]
    if nchars:
        # 长度字段通常是字符数（含或不含结尾 NUL 视实现而定），
        # 用解码后的字符串长度做兜底，避免把结尾的 NUL 算进路径。
        raw = raw[:nchars * 2]
    try:
        original = raw.decode('utf-16-le').rstrip('\x00')
    except UnicodeDecodeError:
        return None
    return {'version': ver, 'size': size, 'original': original}


# ---------------------------------------------------------------- 噪声特征
# 全部用正则。写正则时注意 Windows 路径里的反斜杠——在正则里 \g \w 之类
# 有别的含义，必须写成 \\ 或者用字符类。这里统一用正向写法，别用 \g。
NOISE_PATTERNS = [
    # PowerShell 的执行策略探测文件，每次启动都建 1~4 个。
    # 内容是固定的一句注释，纯粹是 AppLocker 探测用的。
    ('PowerShell 执行策略探测', r'__PSScriptPolicyTest_'),
    # go build 被中断留下的中间产物
    ('go build 临时目录', r'go-build\d+$'),
    # CodeBuddy / WorkBuddy 工具自身的临时物
    ('工具临时目录', r'codebuddy-shell-payload-'),
    ('工具临时目录', r'codebuddy-safe-delete'),
    ('工具临时目录', r'workbuddy-product-spill-'),
    ('工具临时目录', r'workbuddy-conversation-product-'),
    ('工具临时目录', r'workbuddy-sandbox-cli-gc-'),
    # 工具每次调用在 %TEMP% 下建的短随机名目录（6~8 位小写字母/数字/下划线，
    # 如 rj5ot4gb / _xcafdc2 / fxq_xbwd）。
    # 这个模式比较宽，所以额外要求：位于 %TEMP% 根下、且体积很小
    # （工具只往里放一个几百字节的脚本）。两个条件同时满足才算，
    # 避免误伤别的程序建的临时目录。
    ('工具每次调用的临时目录', r'[\\/][Tt]emp[\\/]_?[a-z0-9_]{6,8}$'),
]


def classify(original, size=None):
    """返回这条目属于哪类噪声；不是噪声则返回 None。

    size 用来给"短随机名"那条加约束——只有很小的才算，别的程序也可能
    在 %TEMP% 建随机名目录，但通常有实际内容。
    """
    for label, pat in NOISE_PATTERNS:
        if not re.search(pat, original):
            continue
        # 宽模式再收紧一下
        if label == '工具每次调用的临时目录':
            if size is None or size > 4096:
                continue
        return label
    return None


# ---------------------------------------------------------------- 报告

def collect(dirs):
    """收集所有条目的元数据。$I 是元数据，$R 是内容本体。"""
    items = []
    for d in dirs:
        for meta_path in glob.glob(os.path.join(d, '$I*')):
            m = read_meta(meta_path)
            if not m:
                continue
            # 对应的 $R 文件（同后缀）
            suffix = os.path.basename(meta_path)[2:]
            body = os.path.join(d, '$R' + suffix)
            m['meta'] = meta_path
            m['body'] = body if os.path.exists(body) else None
            m['mtime'] = os.path.getmtime(meta_path)
            items.append(m)
    return items


def cmd_report(items):
    if not items:
        print('回收站是空的。')
        return

    print('回收站共 %d 项\n' % len(items))

    buckets = Counter()
    unknown = []
    for it in items:
        label = classify(it['original'], it['size'])
        if label:
            buckets[label] += 1
        else:
            unknown.append(it)

    if buckets:
        print('=== 已识别的工具噪声（可以安全清掉）===')
        for label, n in buckets.most_common():
            print('  %-24s %5d 项' % (label, n))
        print('  ' + '-' * 34)
        print('  %-24s %5d 项' % ('小计', sum(buckets.values())))
        print()

    if unknown:
        print('=== 其他条目（可能是你自己删的，我没动）===')
        # 只列体积较大的，避免刷屏
        unknown.sort(key=lambda x: -x['size'])
        for it in unknown[:15]:
            when = time.strftime('%m-%d %H:%M', time.localtime(it['mtime']))
            print('  %s  %10s  %s' % (when, human(it['size']), it['original']))
        if len(unknown) > 15:
            print('  ... 另有 %d 项（都很小）' % (len(unknown) - 15))
        print()

    total_size = sum(i['size'] for i in items)
    print('总体积 %s（体积不是问题，条目数才是）' % human(total_size))
    print()
    print('结论：这些都是"每次调用工具就产生几个文件"留下的，不是一次性泄漏。')
    print('要根治得让产生方别往 %TEMP% 写；本脚本负责把已有的清掉。')


def human(n):
    for unit, div in (('G', 1 << 30), ('M', 1 << 20), ('K', 1 << 10)):
        if n >= div:
            return '%.1f%s' % (n / div, unit)
    return '%dB' % n


# ---------------------------------------------------------------- 清理

def cmd_clean(items, do_all):
    """彻底删除条目。

    这里不用 shell 的 rm：回收站里的文件有 $I / $R 成对关系，必须同时删掉
    两个，否则回收站会留下一堆指向不存在内容的元数据（表现为"幽灵条目"，
    点还原会报错）。
    """
    targets = items if do_all else [i for i in items if classify(i['original'], i['size'])]

    if not targets:
        print('没有匹配到需要清理的条目。')
        print('（要连你自己删的文件一起清掉，加 --all）')
        return 0

    print('将彻底删除 %d 项（不进入回收站）：\n' % len(targets))
    freed = 0
    ok = 0

    for it in targets:
        label = classify(it['original'], it['size']) or '用户文件'
        detail = '  [%s] %s' % (label, it['original'])
        errs = []
        # $I 与 $R 必须成对删除
        for p in (it['meta'], it['body']):
            if not p or not os.path.exists(p):
                continue
            for attempt in range(3):
                try:
                    os.chmod(p, 0o700)
                except OSError:
                    pass
                try:
                    if os.path.isdir(p):
                        import shutil
                        shutil.rmtree(p)
                    else:
                        os.remove(p)
                    break
                except OSError as e:
                    errs.append(str(e))
                    time.sleep(0.2)

        if errs:
            print('  失败：%s  (%s)' % (detail, errs[-1]))
        else:
            ok += 1
            freed += it['size']

    print('\n已清理 %d / %d 项，释放约 %s' % (ok, len(targets), human(freed)))
    if ok < len(targets):
        print('有失败的——多半是被占用或有权限保护，重开一次资源管理器再试。')
    return 0


def main():
    ap = argparse.ArgumentParser(description='诊断/清理回收站里的工具噪声')
    ap.add_argument('action', nargs='?', default='report',
                    choices=['report', 'clean'], help='默认 report')
    ap.add_argument('--all', action='store_true',
                    help='clean 时连"非工具噪声"的条目一起清掉')
    args = ap.parse_args()

    dirs = find_recycle_dirs()
    if not dirs:
        print('没找到回收站目录。', file=sys.stderr)
        return 1

    items = collect(dirs)
    if args.action == 'clean':
        return cmd_clean(items, args.all)
    cmd_report(items)
    return 0


if __name__ == '__main__':
    sys.exit(main())
