#!/bin/sh
# 通途桌面客户端一键安装脚本(Linux / macOS)
#
#   curl -fsSL https://raw.githubusercontent.com/weiguang1017/tongtu/main/install.sh | sh
#
# 装的是「桌面客户端」(系统托盘常驻 + 原生窗口),不是浏览器面板版:
#   - macOS:把 TongTu.app 安装到「应用程序」,并处理无开发者证书带来的
#            "已损坏 / 版本不适配"(ad-hoc 重签名 + 去除 quarantine 隔离标记);
#   - Linux:安装桌面二进制到 /usr/local/bin,并注册应用菜单图标与 .desktop。
#
# 可选环境变量:
#   TONGTU_VERSION      指定版本(如 v0.0.7),默认安装最新 Release
#   TONGTU_INSTALL_DIR  Linux 二进制安装目录,默认 /usr/local/bin
#   TONGTU_APP_DIR      macOS 应用安装目录,默认 /Applications
#
# 下载走 GitHub,国内网络受限时可设置代理:
#   https_proxy=http://127.0.0.1:7890 sh install.sh
set -eu

REPO="weiguang1017/tongtu"
INSTALL_DIR="${TONGTU_INSTALL_DIR:-/usr/local/bin}"
APP_DIR="${TONGTU_APP_DIR:-/Applications}"
VERSION="${TONGTU_VERSION:-}"

info() { printf '\033[1;32m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[1;33m提示:\033[0m %s\n' "$1" >&2; }
fail() { printf '\033[1;31m错误:\033[0m %s\n' "$1" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || fail "需要 curl,请先安装"

# 识别系统与架构
case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *) fail "不支持的系统 $(uname -s)(Windows 请到 Release 页下载 *_windows_*_desktop.zip)" ;;
esac

case "$(uname -m)" in
  x86_64|amd64)  arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) fail "不支持的架构 $(uname -m)" ;;
esac

# 未指定版本时,通过 releases/latest 的重定向拿最新标签(不依赖 API 与 jq)
if [ -z "$VERSION" ]; then
  info "查询最新版本..."
  VERSION=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/${REPO}/releases/latest" | sed 's#.*/tag/##')
  [ -n "$VERSION" ] && [ "$VERSION" != "latest" ] || fail "获取最新版本失败,可用 TONGTU_VERSION=v0.0.7 手动指定"
fi

base_url="https://github.com/${REPO}/releases/download/${VERSION}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# 下载 $1 到 $tmp 并按 checksums.txt 校验(拿不到校验文件则跳过,不视为致命错误)
download() {
  file="$1"
  info "下载 ${file} (${VERSION})..."
  curl -fSL --progress-bar -o "${tmp}/${file}" "${base_url}/${file}" \
    || fail "下载失败,请检查网络或代理设置(国内可试 https_proxy=http://127.0.0.1:7890 sh install.sh)"
  if curl -fsSL -o "${tmp}/checksums.txt" "${base_url}/checksums.txt" 2>/dev/null; then
    expected=$(grep " ${file}\$" "${tmp}/checksums.txt" | awk '{print $1}')
    if [ -n "$expected" ]; then
      if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "${tmp}/${file}" | awk '{print $1}')
      else
        actual=$(shasum -a 256 "${tmp}/${file}" | awk '{print $1}')
      fi
      [ "$expected" = "$actual" ] || fail "SHA256 校验失败,文件可能不完整或被篡改"
      info "SHA256 校验通过"
    fi
  fi
}

install_macos() {
  pkg="tongtu_${VERSION}_darwin_${arch}_desktop.app.zip"
  download "$pkg"

  info "解压 TongTu.app ..."
  ( cd "$tmp" && ditto -x -k "$pkg" app ) || fail "解压失败(需要 macOS 自带的 ditto)"
  src="${tmp}/app/TongTu.app"
  [ -d "$src" ] || fail "压缩包内未找到 TongTu.app"

  target="${APP_DIR}/TongTu.app"
  SUDO=""
  [ -w "$APP_DIR" ] || { SUDO="sudo"; info "写入 ${APP_DIR} 需要管理员权限"; }
  $SUDO rm -rf "$target"
  $SUDO cp -R "$src" "$target"

  # 无付费开发者证书的关键处理:
  # 1) 去掉浏览器/网络下载附带的 quarantine 隔离标记,避免"无法验证开发者/已损坏";
  # 2) ad-hoc 重新签名,补齐 Apple Silicon 内核要求的有效签名,消除"版本不适配"与图标禁止标。
  $SUDO xattr -dr com.apple.quarantine "$target" 2>/dev/null || true
  if command -v codesign >/dev/null 2>&1; then
    $SUDO codesign --force --deep --sign - "$target" 2>/dev/null || \
      warn "ad-hoc 签名失败(可忽略);若首次打开报错,右键 →「打开」一次即可"
  fi

  # 顺带把 CLI 软链到 PATH:app 内二进制同时支持 tongtu 各子命令
  bin_in_app="${target}/Contents/MacOS/tongtu"
  if [ -d "$INSTALL_DIR" ] || $SUDO mkdir -p "$INSTALL_DIR" 2>/dev/null; then
    if [ -w "$INSTALL_DIR" ]; then
      ln -sf "$bin_in_app" "${INSTALL_DIR}/tongtu"
    else
      $SUDO ln -sf "$bin_in_app" "${INSTALL_DIR}/tongtu" 2>/dev/null || true
    fi
  fi

  info "通途桌面客户端 ${VERSION} 已安装到 ${target}"
  info "从「启动台 / 应用程序」打开「通途」即可(菜单栏出现托盘图标)"
}

install_linux() {
  if [ "$arch" != "amd64" ]; then
    fail "Linux 桌面版目前仅提供 amd64;$(uname -m) 请用 headless 版:\
下载 tongtu_${VERSION}_linux_${arch}.tar.gz 后运行 tongtu web,或从源码 make desktop 自行构建"
  fi
  command -v tar >/dev/null 2>&1 || fail "需要 tar,请先安装"

  pkg="tongtu_${VERSION}_linux_amd64_desktop.tar.gz"
  download "$pkg"
  tar -xzf "${tmp}/${pkg}" -C "$tmp"
  dir="${tmp}/tongtu_${VERSION}_linux_amd64_desktop"
  binary="${dir}/tongtu"
  [ -f "$binary" ] || fail "压缩包内未找到 tongtu 二进制"
  chmod +x "$binary"

  info "安装到 ${INSTALL_DIR}/tongtu ..."
  if [ -w "$INSTALL_DIR" ]; then SUDO=""; else SUDO="sudo"; info "写入 ${INSTALL_DIR} 需要管理员权限"; fi
  $SUDO install -m 755 "$binary" "${INSTALL_DIR}/tongtu"

  # 注册应用菜单图标与 .desktop(装到用户目录,无需 root)
  share="${HOME}/.local/share"
  if [ -f "${dir}/tongtu.desktop" ]; then
    install -Dm644 "${dir}/tongtu.desktop" "${share}/applications/tongtu.desktop"
  fi
  for size in 48 128 256; do
    icon="${dir}/icons/hicolor/${size}x${size}/apps/tongtu.png"
    [ -f "$icon" ] && install -Dm644 "$icon" \
      "${share}/icons/hicolor/${size}x${size}/apps/tongtu.png"
  done
  command -v update-desktop-database >/dev/null 2>&1 && \
    update-desktop-database "${share}/applications" 2>/dev/null || true

  warn "桌面版需要 GTK3 与 libwebkit2gtk-4.0 运行库;缺失时用 tongtu web(浏览器面板)。"
  info "通途桌面客户端 ${VERSION} 安装完成,应用菜单里搜索「通途」启动"
}

if [ "$os" = "darwin" ]; then
  install_macos
else
  install_linux
fi

case ":$PATH:" in
  *":${INSTALL_DIR}:"*) info "命令行也可用:直接运行 tongtu <子命令>" ;;
  *) warn "${INSTALL_DIR} 不在 PATH 中,如需命令行请将其加入 PATH" ;;
esac
