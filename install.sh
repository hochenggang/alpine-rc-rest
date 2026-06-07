#!/usr/bin/env sh
# install.sh — 一键安装 alpine-rc-rest（Alpine Linux 上管理 OpenRC 服务的 REST API）
# 行为：
#   1. 探测 OS/ARCH（仅支持 Linux）
#   2. 从 GitHub Releases 下载对应二进制（或使用本地 ./dist/）
#   3. 安装到 /usr/local/bin/alpine-rc-rest
#   4. 写入 /etc/init.d/alpine-rc-rest OpenRC 服务
#   5. 打印下一步操作建议
#
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/<owner>/alpine-rc-rest/main/install.sh | sudo sh
#   VERSION=v0.1.0 sudo sh install.sh
#   sudo sh install.sh /path/to/local-binary   # 本地安装（开发用）

set -eu

REPO="${REPO:-hochenggang/alpine-rc-rest}"
VERSION="${VERSION:-}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
SERVICE_NAME="${SERVICE_NAME:-alpine-rc-rest}"
LISTEN_ADDR="${LISTEN_ADDR:-:8080}"
TOKEN="${SERVICE_MANAGER_TOKEN:-}"
PORT="${LISTEN_ADDR##*:}"

# 本地二进制模式
LOCAL_BIN="${1:-}"

log()  { printf '\033[1;34m[install]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[warn]\033[0m %s\n' "$*" >&2; }
err()  { printf '\033[1;31m[error]\033[0m %s\n' "$*" >&2; }

need_root() {
  if [ "$(id -u)" -ne 0 ]; then
    err "需要 root 权限，请使用 sudo 或以 root 运行"
    exit 1
  fi
}

detect_arch() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$arch" in
    x86_64)  goarch=amd64 ;;
    aarch64) goarch=arm64 ;;
    armv7l)  goarch=arm ;;
    i386|i686) goarch=386 ;;
    *)
      err "不支持的架构：$arch"
      exit 1
      ;;
  esac
  case "$os" in
    linux) goos=linux ;;
    *)
      err "此脚本仅支持 Linux（alpine-rc-rest 设计目标为 Alpine Linux）"
      exit 1
      ;;
  esac
  echo "$goos/$goarch"
}

resolve_version() {
  if [ -n "$VERSION" ]; then
    echo "$VERSION"
    return
  fi
  log "未指定 VERSION，查询最新 release ..."
  url="https://api.github.com/repos/${REPO}/releases/latest"
  if command -v curl >/dev/null 2>&1; then
    tag=$(curl -fsSL "$url" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  elif command -v wget >/dev/null 2>&1; then
    tag=$(wget -qO- "$url" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  fi
  if [ -z "${tag:-}" ]; then
    warn "无法获取最新版本，使用 v0.0.0（请手动指定 VERSION）"
    echo "v0.0.0"
  else
    echo "$tag"
  fi
}

download() {
  ver="$1"
  asset="alpine-rc-rest-${os}-${goarch}"
  base="https://github.com/${REPO}/releases/download/${ver}"
  tmpdir=$(mktemp -d)
  trap 'rm -rf "$tmpdir"' EXIT
  log "下载 $base/$asset"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$tmpdir/$asset" "$base/$asset"
  else
    wget -qO "$tmpdir/$asset" "$base/$asset"
  fi
  echo "$tmpdir/$asset"
}

install_binary() {
  src="$1"
  dst="$INSTALL_DIR/alpine-rc-rest"
  install -m 0755 "$src" "$dst"
  log "已安装到 $dst"
}

write_openrc_script() {
  cfg="/etc/conf.d/${SERVICE_NAME}"
  cat > "$cfg" <<EOF
# ${SERVICE_NAME} 配置文件
# 监听地址（默认 :8080）
LISTEN_ADDR="${LISTEN_ADDR}"
# API Token，留空表示禁用鉴权（dev 模式）
SERVICE_MANAGER_TOKEN="${TOKEN}"
EOF
  chmod 0640 "$cfg"

  initd="/etc/init.d/${SERVICE_NAME}"
  cat > "$initd" <<'EOF'
#!/sbin/openrc-run

name="alpine-rc-rest"
description="REST API for managing OpenRC services on Alpine Linux"
command="/usr/local/bin/alpine-rc-rest"
command_user="root"
command_background=true
pidfile="/run/alpine-rc-rest.pid"

depend() {
    need net
    use dns
}

start_pre() {
    if [ -f /etc/conf.d/alpine-rc-rest ]; then
        . /etc/conf.d/alpine-rc-rest
    fi
    export SERVICE_MANAGER_TOKEN
}
EOF
  chmod 0755 "$initd"
  log "已写入 $initd（配置：$cfg）"
}

post_install_tips() {
  cat <<EOF

[✓] 安装完成。

下一步：
  1. （推荐）设置 API Token：
       sudo sed -i 's|^SERVICE_MANAGER_TOKEN=.*|SERVICE_MANAGER_TOKEN="<your-token>"|' /etc/conf.d/alpine-rc-rest
  2. 修改监听地址（默认 :8080）：
       sudo sed -i 's|^LISTEN_ADDR=.*|LISTEN_ADDR="0.0.0.0:8080"|' /etc/conf.d/alpine-rc-rest
  3. 启动并设置开机自启：
       sudo rc-service ${SERVICE_NAME} start
       sudo rc-update add ${SERVICE_NAME} default
  4. 验证：
       curl -s -H "X-API-Token: <your-token>" http://127.0.0.1:${PORT}/api/v1/services

详细文档：https://github.com/${REPO}
EOF
}

main() {
  need_root
  detect_arch
  log "目标平台：$goos/$goarch"

  if [ -n "$LOCAL_BIN" ]; then
    if [ ! -f "$LOCAL_BIN" ]; then
      err "本地二进制不存在：$LOCAL_BIN"
      exit 1
    fi
    install_binary "$LOCAL_BIN"
  else
    ver=$(resolve_version)
    log "使用版本：$ver"
    bin=$(download "$ver")
    install_binary "$bin"
  fi

  write_openrc_script
  post_install_tips
}

main "$@"
