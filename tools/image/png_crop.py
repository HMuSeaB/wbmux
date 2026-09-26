#!/usr/bin/env python3
"""纯 Python 裁剪 PNG（不依赖 Pillow）。

用途
----
wbmux 的图形界面是内嵌网页，验收要靠截图。而截图常常很高（整页上千像素），
直接看会被缩得很小、字读不清。裁一块出来就能看清。

为什么自己写：环境里没有 Pillow，也不该为了看一张图就装依赖。
这里只做 8bit 真彩/带 alpha、非隔行扫描的 PNG——headless 浏览器截出来的
正好就是这个格式，够用。

用法
----
    python png_crop.py <输入.png> <输出.png> <x0> <y0> <x1> <y1>
"""
import struct
import sys
import zlib


def read_png(path):
    d = open(path, "rb").read()
    if d[:8] != b"\x89PNG\r\n\x1a\n":
        raise ValueError("不是 PNG")
    pos, idat = 8, bytearray()
    while pos < len(d):
        ln = struct.unpack(">I", d[pos:pos + 4])[0]
        typ = d[pos + 4:pos + 8]
        data = d[pos + 8:pos + 8 + ln]
        if typ == b"IHDR":
            w, h, bd, ct, _, _, inter = struct.unpack(">IIBBBBB", data)
            if bd != 8 or ct not in (2, 6) or inter:
                raise ValueError("只支持 8bit 真彩且非隔行，实际 bd=%d ct=%d inter=%d" % (bd, ct, inter))
            ch = 3 if ct == 2 else 4
        elif typ == b"IDAT":
            idat += data
        elif typ == b"IEND":
            break
        pos += 12 + ln

    raw = zlib.decompress(bytes(idat))
    stride = w * ch
    out = bytearray(w * h * ch)
    prev = bytearray(stride)
    p = 0
    for y in range(h):
        ft = raw[p]
        p += 1
        line = bytearray(raw[p:p + stride])
        p += stride
        if ft == 1:
            for i in range(ch, stride):
                line[i] = (line[i] + line[i - ch]) & 255
        elif ft == 2:
            for i in range(stride):
                line[i] = (line[i] + prev[i]) & 255
        elif ft == 3:
            for i in range(stride):
                a = line[i - ch] if i >= ch else 0
                line[i] = (line[i] + ((a + prev[i]) >> 1)) & 255
        elif ft == 4:
            for i in range(stride):
                a = line[i - ch] if i >= ch else 0
                b = prev[i]
                c = prev[i - ch] if i >= ch else 0
                pa, pb, pc = abs(b - c), abs(a - c), abs(a + b - 2 * c)
                pr = a if (pa <= pb and pa <= pc) else (b if pb <= pc else c)
                line[i] = (line[i] + pr) & 255
        out[y * stride:(y + 1) * stride] = line
        prev = line
    return w, h, ch, out


def write_png(path, w, h, ch, px):
    stride = w * ch
    raw = bytearray()
    for y in range(h):
        raw.append(0)          # 全部用 filter 0，简单且通用
        raw += px[y * stride:(y + 1) * stride]

    def chunk(t, data):
        return struct.pack(">I", len(data)) + t + data + struct.pack(">I", zlib.crc32(t + data) & 0xFFFFFFFF)

    ct = 2 if ch == 3 else 6
    blob = b"\x89PNG\r\n\x1a\n"
    blob += chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, ct, 0, 0, 0))
    blob += chunk(b"IDAT", zlib.compress(bytes(raw), 6))
    blob += chunk(b"IEND", b"")
    open(path, "wb").write(blob)


def main(argv):
    if len(argv) != 7:
        print(__doc__)
        return 2
    src, dst = argv[1], argv[2]
    x0, y0, x1, y1 = (int(v) for v in argv[3:7])
    w, h, ch, px = read_png(src)
    x1, y1 = min(x1, w), min(y1, h)
    cw, chh = x1 - x0, y1 - y0
    crop = bytearray(cw * chh * ch)
    for y in range(chh):
        src_off = ((y0 + y) * w + x0) * ch
        crop[y * cw * ch:(y + 1) * cw * ch] = px[src_off:src_off + cw * ch]
    write_png(dst, cw, chh, ch, crop)
    print("裁剪完成 %dx%d -> %dx%d" % (w, h, cw, chh))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
