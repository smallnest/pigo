#!/bin/sh
# pigo 安装脚本：检测当前操作系统 / 架构，从 GitHub Releases 下载最新的
# 预编译二进制，并安装到常用的 PATH 目录。
#
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/smallnest/pigo/master/install.sh | sh
#
# 可用环境变量覆盖默认行为：
#   PIGO_VERSION   指定版本（形如 v0.2.0），默认取最新 release
#   PIGO_INSTALL_DIR  安装目录，默认 /usr/local/bin（无写权限时回退到 ~/.local/bin）
#   GITHUB_TOKEN   可选，用于提高 GitHub API 速率限制
set -eu

REPO="smallnest/pigo"
BINARY="pigo"

info() { printf '%s\n' "pigo-install: $*" >&2; }
err()  { printf '%s\n' "pigo-install: error: $*" >&2; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || err "缺少依赖命令: $1"; }

# 1. 检测下载器（curl 或 wget）。
if command -v curl >/dev/null 2>&1; then
	DL="curl -fsSL"
	DLO="curl -fsSL -o"
elif command -v wget >/dev/null 2>&1; then
	DL="wget -qO-"
	DLO="wget -qO"
else
	err "需要 curl 或 wget"
fi
need tar
need uname

# 2. 检测 OS，映射到 goreleaser 的归档命名（见 .goreleaser.yaml）。
os_raw=$(uname -s)
case "$os_raw" in
	Linux)  OS="Linux" ;;
	Darwin) OS="Darwin" ;;
	MINGW* | MSYS* | CYGWIN* | Windows_NT)
		err "Windows 请从 Releases 页面下载 .zip：https://github.com/$REPO/releases" ;;
	*) err "不支持的操作系统: $os_raw" ;;
esac

# 3. 检测架构，映射到归档命名（amd64→x86_64，386→i386，arm64 保持）。
arch_raw=$(uname -m)
case "$arch_raw" in
	x86_64 | amd64) ARCH="x86_64" ;;
	arm64 | aarch64) ARCH="arm64" ;;
	i386 | i686) ARCH="i386" ;;
	*) err "不支持的架构: $arch_raw" ;;
esac

# 4. 解析目标版本：优先 PIGO_VERSION，否则查询最新 release 的 tag。
VERSION="${PIGO_VERSION:-}"
api_auth=""
[ -n "${GITHUB_TOKEN:-}" ] && api_auth="-H Authorization:\ Bearer\ $GITHUB_TOKEN"
if [ -z "$VERSION" ]; then
	info "查询最新 release ..."
	# 从 GitHub API 的 latest 端点提取 tag_name。
	latest_json=$($DL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null) || \
		err "无法访问 GitHub API，请检查网络或用 PIGO_VERSION 指定版本"
	VERSION=$(printf '%s' "$latest_json" | grep -o '"tag_name"[ ]*:[ ]*"[^"]*"' | head -n1 | sed 's/.*"tag_name"[ ]*:[ ]*"\([^"]*\)".*/\1/')
	[ -n "$VERSION" ] || err "无法解析最新版本号，请用 PIGO_VERSION 指定"
fi

# 归档名里的版本号不带前导 v（goreleaser 的 .Version）。
VER_NUM=$(printf '%s' "$VERSION" | sed 's/^v//')
ARCHIVE="${BINARY}_${VER_NUM}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$REPO/releases/download/$VERSION/$ARCHIVE"

info "版本: $VERSION"
info "平台: ${OS}/${ARCH}"
info "下载: $URL"

# 5. 下载并解压到临时目录。
TMP=$(mktemp -d 2>/dev/null || mktemp -d -t pigo-install)
trap 'rm -rf "$TMP"' EXIT INT TERM
$DLO "$TMP/$ARCHIVE" "$URL" || err "下载失败: $URL"
tar -xzf "$TMP/$ARCHIVE" -C "$TMP" || err "解压失败: $ARCHIVE"
[ -f "$TMP/$BINARY" ] || err "归档中未找到二进制 $BINARY"
chmod +x "$TMP/$BINARY"

# 6. 选择安装目录：PIGO_INSTALL_DIR > /usr/local/bin > ~/.local/bin。
DIR="${PIGO_INSTALL_DIR:-}"
if [ -z "$DIR" ]; then
	if [ -w /usr/local/bin ] 2>/dev/null; then
		DIR="/usr/local/bin"
	elif [ "$(id -u)" = "0" ]; then
		DIR="/usr/local/bin"
	else
		DIR="$HOME/.local/bin"
	fi
fi
mkdir -p "$DIR" 2>/dev/null || err "无法创建安装目录: $DIR"

# 7. 安装。若目录不可写但可 sudo，尝试用 sudo。
DEST="$DIR/$BINARY"
if [ -w "$DIR" ]; then
	mv "$TMP/$BINARY" "$DEST"
elif command -v sudo >/dev/null 2>&1; then
	info "$DIR 需要提升权限，使用 sudo 安装 ..."
	sudo mv "$TMP/$BINARY" "$DEST"
else
	err "$DIR 不可写且无 sudo，请设置 PIGO_INSTALL_DIR 指向可写目录"
fi

info "已安装: $DEST"

# 8. 提示 PATH 是否包含安装目录。
case ":$PATH:" in
	*":$DIR:"*) : ;;
	*) info "注意: $DIR 不在 PATH 中，请将其加入 PATH，例如：" >&2
	   info "  echo 'export PATH=\"$DIR:\$PATH\"' >> ~/.profile" >&2 ;;
esac

"$DEST" --version || true
