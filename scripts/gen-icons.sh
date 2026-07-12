#!/bin/sh
# 从 assets/icon/*.svg 生成全部图标产物(make icons 调用,在 macOS 上运行)。
# 依赖: rsvg-convert(brew install librsvg)、iconutil(系统自带)、python3、
#       go-winres(经 go run 调用,无需安装)。
# 生成物全部提交进仓库,CI 构建零图形依赖。
set -eu
cd "$(dirname "$0")/.."

SVG=assets/icon/tongtu.svg
TRAY_SVG=assets/icon/tray-template.svg
PNG_DIR=assets/icon/png

command -v rsvg-convert >/dev/null || {
	echo "缺少 rsvg-convert,请先: brew install librsvg" >&2
	exit 1
}

# 1. 主图标各尺寸 PNG
mkdir -p "$PNG_DIR"
for size in 16 32 48 64 128 256 512 1024; do
	rsvg-convert -w $size -h $size "$SVG" -o "$PNG_DIR/icon-$size.png"
done

# 2. macOS .icns
ICONSET=$(mktemp -d)/tongtu.iconset
mkdir -p "$ICONSET"
cp "$PNG_DIR/icon-16.png" "$ICONSET/icon_16x16.png"
cp "$PNG_DIR/icon-32.png" "$ICONSET/icon_16x16@2x.png"
cp "$PNG_DIR/icon-32.png" "$ICONSET/icon_32x32.png"
cp "$PNG_DIR/icon-64.png" "$ICONSET/icon_32x32@2x.png"
cp "$PNG_DIR/icon-128.png" "$ICONSET/icon_128x128.png"
cp "$PNG_DIR/icon-256.png" "$ICONSET/icon_128x128@2x.png"
cp "$PNG_DIR/icon-256.png" "$ICONSET/icon_256x256.png"
cp "$PNG_DIR/icon-512.png" "$ICONSET/icon_256x256@2x.png"
cp "$PNG_DIR/icon-512.png" "$ICONSET/icon_512x512.png"
cp "$PNG_DIR/icon-1024.png" "$ICONSET/icon_512x512@2x.png"
iconutil -c icns "$ICONSET" -o assets/icon/tongtu.icns
echo "assets/icon/tongtu.icns 已生成"

# 3. Windows 主 .ico + syso(图标+版本信息+manifest,go build 自动链入)
python3 scripts/png2ico.py assets/icon/tongtu.ico \
	"$PNG_DIR/icon-16.png" "$PNG_DIR/icon-32.png" "$PNG_DIR/icon-48.png" \
	"$PNG_DIR/icon-64.png" "$PNG_DIR/icon-128.png" "$PNG_DIR/icon-256.png"
VERSION=$(git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo 0.0.0)
go run github.com/tc-hib/go-winres@v0.3.3 make \
	--in assets/icon/winres.json \
	--out cmd/tongtu/rsrc \
	--arch amd64,arm64 \
	--file-version "$VERSION" --product-version "$VERSION"
echo "cmd/tongtu/rsrc_windows_*.syso 已生成(版本 $VERSION)"

# 4. 托盘图标
rsvg-convert -w 44 -h 44 "$TRAY_SVG" -o internal/desktop/icons/tray_template.png
rsvg-convert -w 32 -h 32 "$SVG" -o internal/desktop/icons/tray_color.png
python3 scripts/png2ico.py internal/desktop/icons/tray.ico \
	"$PNG_DIR/icon-16.png" "$PNG_DIR/icon-32.png"

# 5. 窗口子进程的 Dock 图标(macOS)
cp "$PNG_DIR/icon-256.png" internal/winview/dockicon.png

echo "全部图标已生成"
