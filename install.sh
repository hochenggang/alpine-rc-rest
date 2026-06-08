#!/usr/bin/env sh
# install.sh — 一键安装 alpine-rc-rest（Alpine Linux 上管理 OpenRC 服务的 REST API）
#
# 行为：
#   1. 询问是否安装 [Y/n]（默认 Y）
#   2. 尝试从 GitHub Releases 获取最新版本与对应 amd64 资产 URL
#   3. 询问用户是否提供自定义 URL（回车使用默认 GitHub 链接）
#   4. 下载 → 装到 /usr/local/bin/alpine-rc-rest
#   5. 随机生成 128-bit token 写入 /etc/conf.d/alpine-rc-rest
#   6. 写入 /etc/init.d/alpine-rc-rest
#   7. 打印 token + 下一步操作
#
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/<owner>/alpine-rc-rest/main/install.sh | sudo sh
#   VERSION=v0.1.0 sudo sh install.sh
#   非交互：YES=1 URL=https://... TOKEN=<hex32> sudo sh install.sh

set -eu

REPO="${REPO:-hochenggang/alpine-rc-rest}"
VERSION="${VERSION:-}"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
SERVICE_NAME="${SERVICE_NAME:-alpine-rc-rest}"
LISTEN_ADDR="${LISTEN_ADDR:-:8080}"
NONINTERACTIVE="${YES:-0}"

# ---------- UI ----------

log()  { printf '\033[1;34m[install]\033[0m %s\n' "$*" >&2; }
warn() { printf '\033[1;33m[warn]\033[0m    %s\n' "$*" >&2; }
err()  { printf '\033[1;31m[error]\033[0m  %s\n' "$*" >&2; }

# confirm <prompt> [default_yes=1]  询问 Y/n；回车 = 默认
confirm() {
  prompt="$1"
  default_yes="${2:-1}"
  if [ "$default_yes" = "1" ]; then
    suffix="[Y/n]"; def="Y"
  else
    suffix="[y/N]"; def="N"
  fi
  printf "%s %s " "$prompt" "$suffix"
  if [ "$NONINTERACTIVE" = "1" ]; then
    printf "%s (non-interactive)\n" "$def"
    [ "$def" = "Y" ]
    return $?
  fi
  read -r ans || true
  if [ -z "$ans" ]; then ans=$def; fi
  case "$ans" in
    Y|y|yes|YES|Yes) return 0 ;;
    N|n|no|NO|No)    return 1 ;;
    *)               return 1 ;;
  esac
}

# ask_url <default>  询问 URL；回车 = 默认
#   提示/默认信息写到 stderr，避免被命令替换 $(ask_url ...) 误捕获
ask_url() {
  default="$1"
  printf "直接回车后将拉取 [%s]\n" "$default" >&2
  printf "如需自定义二进制文件链接，请输入后再回车。\n" >&2
  printf "[等待用户输入:] " >&2
  if [ "$NONINTERACTIVE" = "1" ]; then
    printf "%s (non-interactive)\n" "$default" >&2
    printf "%s" "$default"
    return
  fi
  IFS= read -r ans < /dev/tty || ans=""
  if [ -z "$ans" ]; then
    printf "%s" "$default"
  else
    printf "%s" "$ans"
  fi
}

# gen_token_128bit 输出 32 个十六进制字符（= 128 bit）
gen_token_128bit() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 16
    return
  fi
  if [ -r /dev/urandom ]; then
    head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n'
    return
  fi
  # 兜底：从 uuid 去连字符
  cat /proc/sys/kernel/random/uuid 2>/dev/null | tr -d - | head -c 32
}

# ---------- 检查 ----------

need_root() {
  if [ "$(id -u)" -ne 0 ]; then
    err "需要 root 权限，请使用 sudo 或以 root 运行"
    exit 1
  fi
}

detect_arch() {
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$os" in
    linux) ;;
    *) err "此脚本仅支持 Linux（alpine-rc-rest 设计目标为 Alpine Linux）"; exit 1 ;;
  esac
  case "$arch" in
    x86_64) goarch=amd64 ;;
    *)
      err "本仓库 release 仅提供 linux/amd64（你的架构：$arch）。"
      err "如需其他架构请自行 cross-compile 或在 PR 中扩展 matrix。"
      exit 1
      ;;
  esac
  goos=linux
  log "目标平台：$goos/$goarch"
}

# resolve_latest_release 尝试从 GitHub 拿最新 release 的 tag_name
# 成功：echo "<tag>"; 失败：echo "" 并 warn
resolve_latest_release() {
  url="https://api.github.com/repos/${REPO}/releases/latest"
  body=""
  if command -v curl >/dev/null 2>&1; then
    body=$(curl -fsSL "$url" 2>/dev/null || true)
  elif command -v wget >/dev/null 2>&1; then
    body=$(wget -qO- "$url" 2>/dev/null || true)
  fi
  if [ -z "$body" ]; then
    warn "无法访问 GitHub API（无网络 / 限流）"
    return 1
  fi
  tag=$(printf '%s' "$body" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  if [ -z "$tag" ]; then
    warn "GitHub API 响应未包含 tag_name"
    return 1
  fi
  printf '%s' "$tag"
}

# validate_url 仅允许 http/https
validate_url() {
  case "$1" in
    http://*|https://*) return 0 ;;
    *) return 1 ;;
  esac
}

download() {
  url="$1"
  tmpdir=$(mktemp -d)
  trap 'rm -rf "$tmpdir"' EXIT
  asset="alpine-rc-rest-${goos}-${goarch}"
  out="$tmpdir/$asset"
  log "下载 $url"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL -o "$out" "$url"
  else
    wget -qO "$out" "$url"
  fi
  printf '%s' "$out"
}

install_binary() {
  src="$1"
  dst="$INSTALL_DIR/alpine-rc-rest"
  install -m 0755 "$src" "$dst"
  log "已安装到 $dst"
}

# write_conf_d 写入 /etc/conf.d/<name>：LISTEN_ADDR 与 SERVICE_MANAGER_TOKEN
write_conf_d() {
  token="$1"
  cfg="/etc/conf.d/${SERVICE_NAME}"
  cat > "$cfg" <<EOF
# ${SERVICE_NAME} 配置文件
# 监听地址（默认 :8080）
LISTEN_ADDR="${LISTEN_ADDR}"
# API Token；install.sh 自动生成
SERVICE_MANAGER_TOKEN="${token}"
EOF
  chmod 0640 "$cfg"
  log "已写入 $cfg"
}

# write_openrc_script 写入 /etc/init.d/<name>
write_openrc_script() {
  initd="/etc/init.d/${SERVICE_NAME}"
  cat > "$initd" <<'EOF'
#!/sbin/openrc-run

name='alpine-rc-rest'
description='REST API for managing OpenRC services on Alpine Linux'
command='/usr/local/bin/alpine-rc-rest'
command_user='root'
command_background=true
pidfile='/run/alpine-rc-rest.pid'

depend() {
    need net
    use dns
}

start_pre() {
    if [ -f /etc/conf.d/alpine-rc-rest ]; then
        . /etc/conf.d/alpine-rc-rest
    fi
    export SERVICE_MANAGER_TOKEN
    export LISTEN_ADDR
}
EOF
  chmod 0755 "$initd"
  log "已写入 $initd"
}

# prepare_reinstall 检测已安装并：
#   1. stop 旧服务（容忍失败）
#   2. 从所有运行级别移除（容忍失败）
#   3. 从 /etc/conf.d 中读旧 token 沿用（避免重装后鉴权失效）
prepare_reinstall() {
  initd="/etc/init.d/${SERVICE_NAME}"
  conf="/etc/conf.d/${SERVICE_NAME}"
  if [ -f "$initd" ]; then
    log "检测到已安装：停止旧服务（容忍失败）"
    rc-service ${SERVICE_NAME} stop 2>/dev/null || true
    rc-update del ${SERVICE_NAME} 2>/dev/null || true
  fi
  # 沿用旧 token（仅当未通过环境变量显式提供时）
  if [ -z "${TOKEN:-}" ] && [ -f "$conf" ]; then
    cur=$(sed -n 's/^SERVICE_MANAGER_TOKEN=["]\{0,1\}\([^"]*\)["]\{0,1\}$/\1/p' "$conf" | head -n1)
    if [ "${#cur}" -eq 32 ]; then
      TOKEN="$cur"
      log "沿用旧 token（来自 $conf）"
    fi
  fi
}

post_install_tips() {
  token="$1"
  port="${LISTEN_ADDR##*:}"
  cat <<EOF

[✓] 安装完成。

================================================================
  API Token
    持久化位置：/etc/conf.d/${SERVICE_NAME}
    当前值    ：${token}
    重新获取  ：sudo grep SERVICE_MANAGER_TOKEN /etc/conf.d/${SERVICE_NAME}
================================================================

下一步：
  1. 启动并设置开机自启：
       sudo rc-service ${SERVICE_NAME} start
       sudo rc-update add ${SERVICE_NAME} default
  2. 验证：
       curl -s -H "X-API-Token: \${token}" \\
            http://127.0.0.1:${port}/api/v1/project
  3. 部署一个项目（示例）：
       curl -X POST http://127.0.0.1:${port}/api/v1/project \\
            -H "X-API-Token: \${token}" \\
            -H "Content-Type: application/json" \\
            -d '{
              "project_name": "myapi",
              "bin_url":      "https://example.com/myapi-linux-amd64",
              "env": { "PORT": "8080" }
            }'

详细文档：https://github.com/${REPO}
EOF
}

# ---------- 主流程 ----------

main() {
  need_root
  detect_arch

  cat <<'BANNER'
────────────────────────────────────────────
   alpine-rc-rest 安装器
   极简 · 零依赖 · OpenRC 进程管理 REST API
────────────────────────────────────────────
BANNER

  # 1. 安装确认
  if ! confirm "Install alpine-rc-rest?" 1; then
    log "已取消"
    exit 0
  fi

  # 1.5. 检测重装：stop 旧服务、沿用旧 token
  prepare_reinstall

  # 2. 确定版本
  if [ -z "$VERSION" ]; then
    log "查询最新 release ..."
    if VERSION=$(resolve_latest_release); then
      [ -n "$VERSION" ] && log "检测到最新版本：$VERSION"
    fi
    if [ -z "$VERSION" ]; then
      warn "无法自动获取版本；请设置 VERSION 环境变量（如 VERSION=v0.1.0）后重试。"
      exit 1
    fi
  fi
  log "使用版本：$VERSION"

  # 3. 计算默认 URL + URL 提示
  default_url="https://github.com/${REPO}/releases/download/${VERSION}/alpine-rc-rest-${goos}-${goarch}"
  url=$(ask_url "$default_url")
  if [ -z "$url" ]; then url="$default_url"; fi
  if ! validate_url "$url"; then
    err "非法 URL（仅支持 http/https）：$url"
    exit 1
  fi
  log "目标 URL：$url"

  # 4. 下载 + 装包
  bin=$(download "$url")
  install_binary "$bin"

  # 5. token
  if [ -n "${TOKEN:-}" ]; then
    log "使用环境变量提供的 TOKEN"
  else
    TOKEN=$(gen_token_128bit)
  fi
  if [ "${#TOKEN}" -ne 32 ]; then
    err "TOKEN 长度异常（期望 32 个 hex 字符，实际 ${#TOKEN}）"
    exit 1
  fi

  # 6. 写配置 + OpenRC
  write_conf_d "$TOKEN"
  write_openrc_script

  # 7. 打印 token
  post_install_tips "$TOKEN"
}

main "$@"
