#!/usr/bin/env python3
"""把若干 PNG 合成一个多尺寸 .ico(make icons 用,无第三方依赖)。

小尺寸(<256)转为无压缩 BMP 条目 —— Windows LoadImage 对 BMP 条目
兼容性最好(托盘图标路径);256 及以上直接内嵌 PNG 条目(Vista+ 支持)。
仅支持 rsvg-convert 输出的 8-bit RGBA 非隔行 PNG。

用法: png2ico.py 输出.ico 输入1.png [输入2.png ...]
"""
import struct
import sys
import zlib


def decode_png(data: bytes):
    """返回 (宽, 高, RGBA 像素行列表)。"""
    assert data[:8] == b"\x89PNG\r\n\x1a\n", "不是 PNG 文件"
    pos, width, height, idat = 8, 0, 0, b""
    while pos < len(data):
        (length,) = struct.unpack(">I", data[pos : pos + 4])
        ctype = data[pos + 4 : pos + 8]
        body = data[pos + 8 : pos + 8 + length]
        if ctype == b"IHDR":
            width, height, depth, color, _, _, interlace = struct.unpack(">IIBBBBB", body)
            assert depth == 8 and color == 6 and interlace == 0, "仅支持 8-bit RGBA 非隔行 PNG"
        elif ctype == b"IDAT":
            idat += body
        pos += 12 + length

    raw = zlib.decompress(idat)
    stride = width * 4
    rows, prev = [], bytearray(stride)
    for y in range(height):
        base = y * (stride + 1)
        ftype = raw[base]
        line = bytearray(raw[base + 1 : base + 1 + stride])
        for x in range(stride):
            a = line[x - 4] if x >= 4 else 0  # 左
            b = prev[x]  # 上
            c = prev[x - 4] if x >= 4 else 0  # 左上
            if ftype == 1:
                line[x] = (line[x] + a) & 0xFF
            elif ftype == 2:
                line[x] = (line[x] + b) & 0xFF
            elif ftype == 3:
                line[x] = (line[x] + (a + b) // 2) & 0xFF
            elif ftype == 4:  # Paeth
                p = a + b - c
                pa, pb, pc = abs(p - a), abs(p - b), abs(p - c)
                pred = a if (pa <= pb and pa <= pc) else (b if pb <= pc else c)
                line[x] = (line[x] + pred) & 0xFF
        rows.append(bytes(line))
        prev = line
    return width, height, rows


def bmp_entry(width: int, height: int, rows) -> bytes:
    """RGBA 行 → ICO 的 BMP 条目(BITMAPINFOHEADER + 自下而上 BGRA + AND 掩码)。"""
    header = struct.pack("<IiiHHIIiiII", 40, width, height * 2, 1, 32, 0, 0, 0, 0, 0, 0)
    pixels = b"".join(
        bytes(v for x in range(width) for v in (row[x * 4 + 2], row[x * 4 + 1], row[x * 4], row[x * 4 + 3]))
        for row in reversed(rows)
    )
    mask_stride = ((width + 31) // 32) * 4  # 1bpp 行按 32 位对齐;全 0 = 完全由 alpha 决定
    return header + pixels + b"\x00" * (mask_stride * height)


def main() -> None:
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    out, srcs = sys.argv[1], sys.argv[2:]

    entries = []  # (宽, 高, 条目字节)
    for path in srcs:
        with open(path, "rb") as f:
            data = f.read()
        width, height, rows = decode_png(data)
        entries.append((width, height, data if width >= 256 else bmp_entry(width, height, rows)))

    directory, blobs, offset = b"", b"", 6 + 16 * len(entries)
    for width, height, blob in entries:
        directory += struct.pack(
            "<BBBBHHII", width % 256, height % 256, 0, 0, 1, 32, len(blob), offset
        )
        blobs += blob
        offset += len(blob)
    with open(out, "wb") as f:
        f.write(struct.pack("<HHH", 0, 1, len(entries)) + directory + blobs)
    print(f"{out}: {len(entries)} 个尺寸 <- {', '.join(srcs)}")


if __name__ == "__main__":
    main()
