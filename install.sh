#!/bin/sh
# 通途一键安装脚本(Linux / macOS)
#
#   curl -fsSL https://raw.githubusercontent.com/weiguang1017/tongtu/main/install.sh | sh
#
# 可选环境变量:
#   TONGTU_VERSION      指定版本(如 v0.0.2),默认安装最新 Release
#   TONGTU_INSTALL_DIR  安装目录,默认 /usr/local/bin
#
# 下载走 GitHub,国内网络受限时可设置代理:
#   https_proxy=http://127.0.0.1:7890 sh install.sh
set -eu

REPO="weiguang1017/tongtu"
INSTALL_DIR="${TONGTU_INSTALL_DIR:-/usr/local/bin}"
VERSION="${TONGTU_VERSION:-}"

info() { printf '\033[1;32m==>\033[0m %s\n' "$1"; }
fail() { printf '\033[1;31m错误:\033[0m %s\n' "$1" >&2; exit 1; }

command -v curl >/dev/null 2>&1 || fail "需要 curl,请先安装"
command -v tar  >/dev/null 2>&1 || fail "需要 tar,请先安装"

# 识别系统与架构
case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux)  os="linux" ;;
  *) fail "不支持的系统 $(uname -s)(Windows 请到 Release 页下载 zip 包)" ;;
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
  [ -n "$VERSION" ] && [ "$VERSION" != "latest" ] || fail "获取最新版本失败,可用 TONGTU_VERSION=v0.0.2 手动指定"
fi

package="tongtu_${VERSION}_${os}_${arch}"
base_url="https://github.com/${REPO}/releases/download/${VERSION}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

info "下载 ${package}.tar.gz (${VERSION})..."
curl -fSL --progress-bar -o "${tmp}/${package}.tar.gz" "${base_url}/${package}.tar.gz" \
  || fail "下载失败,请检查网络或代理设置"

# 校验 SHA256(checksums.txt 拿不到时跳过,不视为致命错误)
if curl -fsSL -o "${tmp}/checksums.txt" "${base_url}/checksums.txt" 2>/dev/null; then
  expected=$(grep " ${package}.tar.gz\$" "${tmp}/checksums.txt" | awk '{print $1}')
  if command -v sha256sum >/dev/null 2>&1; then
    actual=$(sha256sum "${tmp}/${package}.tar.gz" | awk '{print $1}')
  else
    actual=$(shasum -a 256 "${tmp}/${package}.tar.gz" | awk '{print $1}')
  fi
  [ "$expected" = "$actual" ] || fail "SHA256 校验失败,文件可能不完整或被篡改"
  info "SHA256 校验通过"
else
  info "未获取到 checksums.txt,跳过校验"
fi

tar -xzf "${tmp}/${package}.tar.gz" -C "$tmp"
binary="${tmp}/${package}/tongtu"
[ -f "$binary" ] || fail "压缩包内未找到 tongtu 二进制"
chmod +x "$binary"

# macOS:清除浏览器下载可能附带的 quarantine 标记(curl 下载本不带,防御性处理)
if [ "$os" = "darwin" ] && command -v xattr >/dev/null 2>&1; then
  xattr -d com.apple.quarantine "$binary" 2>/dev/null || true
fi

info "安装到 ${INSTALL_DIR}/tongtu ..."
if [ -w "$INSTALL_DIR" ]; then
  install -m 755 "$binary" "${INSTALL_DIR}/tongtu"
else
  info "写入 ${INSTALL_DIR} 需要管理员权限"
  sudo install -m 755 "$binary" "${INSTALL_DIR}/tongtu"
fi

"${INSTALL_DIR}/tongtu" help >/dev/null 2>&1 || fail "安装后的二进制无法运行"
info "通途 ${VERSION} 安装完成"
case ":$PATH:" in
  *":${INSTALL_DIR}:"*) info "运行 tongtu 即可启动图形界面" ;;
  *) info "注意:${INSTALL_DIR} 不在 PATH 中,请将其加入 PATH 后使用" ;;
esac
