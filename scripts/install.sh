#!/usr/bin/env bash
# Grok-Register 一键部署
#
# Linux (Debian/Ubuntu，需 root/sudo):
#   curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | sudo bash
#
# macOS（需已装 Homebrew + Docker Desktop，普通用户即可）:
#   curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | bash
#
# 自定义:
#   curl -fsSL ... | sudo bash -s -- --command grok-reg --install-dir /opt/Grok-Register
#   curl -fsSL ... | bash -s -- --command grok --home "$HOME/.grok"
#
# 选项 / 环境变量见 --help。

set -euo pipefail

# ---------------------------------------------------------------------------
# OS 探测（尽早）
# ---------------------------------------------------------------------------
OS_RAW="$(uname -s 2>/dev/null || echo unknown)"
case "$OS_RAW" in
  Darwin) OS=darwin ;;
  Linux)  OS=linux ;;
  *)
    printf '[x] 不支持的系统: %s（仅 Linux / macOS）\n' "$OS_RAW" >&2
    exit 1
    ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) GO_ARCH=amd64 ;;
  aarch64|arm64) GO_ARCH=arm64 ;;
  *)
    printf '[x] 暂不支持架构: %s（仅 amd64/arm64）\n' "$ARCH" >&2
    exit 1
    ;;
esac

# ---------------------------------------------------------------------------
# 默认值（可被环境变量或 CLI 覆盖；macOS 默认装在用户目录）
# ---------------------------------------------------------------------------
COMMAND_NAME="${COMMAND_NAME:-grok}"
REPO_URL="${REPO_URL:-https://github.com/Charles-0509/Grok-Register.git}"
BRANCH="${BRANCH:-main}"
GO_VERSION="${GO_VERSION:-1.24.4}"
SKIP_DOCKER="${SKIP_DOCKER:-0}"
SKIP_CLEARANCE="${SKIP_CLEARANCE:-0}"
SKIP_BROWSER="${SKIP_BROWSER:-0}"
SKIP_GO_INSTALL="${SKIP_GO_INSTALL:-0}"
START_CLEARANCE="${START_CLEARANCE:-1}"

# 路径：若调用方未通过环境变量指定，按 OS 给默认
if [ "$OS" = "darwin" ]; then
  _HOME="${HOME:-/Users/$(id -un)}"
  INSTALL_DIR="${INSTALL_DIR:-${_HOME}/Grok-Register}"
  GROK_HOME_OPT="${GROK_HOME:-${_HOME}/.grok}"
  BIN_DIR="${BIN_DIR:-${_HOME}/.local/bin}"
  SHARE_DIR="${SHARE_DIR:-${_HOME}/.local/share/grok-reg}"
  VENV_DIR="${VENV_DIR:-${_HOME}/.local/share/cloakbrowser-venv}"
else
  INSTALL_DIR="${INSTALL_DIR:-/opt/Grok-Register}"
  GROK_HOME_OPT="${GROK_HOME:-}"
  BIN_DIR="${BIN_DIR:-/usr/local/bin}"
  SHARE_DIR="${SHARE_DIR:-/usr/local/share/grok-reg}"
  VENV_DIR="${VENV_DIR:-/opt/cloakbrowser-venv}"
fi

usage() {
  cat <<EOF
Grok-Register 一键部署

平台:
  Linux  Debian/Ubuntu — 需 root/sudo，自动装 Go/Docker/系统库
  macOS  需已安装 Homebrew + Docker Desktop；缺则提示安装命令后退出
         默认装到用户目录（无需 sudo）

用法:
  install.sh [选项]

选项:
  --command NAME        CLI 命令名（默认 grok）
  --install-dir PATH    源码目录
                          Linux 默认 /opt/Grok-Register
                          macOS 默认 ~/Grok-Register
  --home PATH           数据目录 GROK_HOME
                          Linux 默认 /root/.grok
                          macOS 默认 ~/.grok
  --bin-dir PATH        二进制目录
                          Linux 默认 /usr/local/bin
                          macOS 默认 ~/.local/bin
  --share-dir PATH      mint 脚本目录
  --venv-dir PATH       Python venv 路径
  --repo URL            Git 仓库
  --branch NAME         分支（默认 main）
  --go-version VER      Linux 官方 tarball Go 版本（默认 ${GO_VERSION}）
  --skip-docker         不安装/不检查 Docker
  --skip-clearance      不起 clearance
  --skip-browser        不装 Playwright/CloakBrowser
  --skip-go             不自动安装 Go
  --no-start-clearance  不 docker compose up
  -h, --help            帮助

示例:
  # Linux
  curl -fsSL .../install.sh | sudo bash
  curl -fsSL .../install.sh | sudo bash -s -- --command grok-reg

  # macOS（先装 brew + Docker Desktop）
  curl -fsSL .../install.sh | bash
  curl -fsSL .../install.sh | bash -s -- --command grok-reg --bin-dir "\$(brew --prefix)/bin"
EOF
}

log()  { printf '[*] %s\n' "$*"; }
ok()   { printf '[✓] %s\n' "$*"; }
warn() { printf '[!] %s\n' "$*" >&2; }
die()  { printf '[x] %s\n' "$*" >&2; exit 1; }

# ---------------------------------------------------------------------------
# 参数解析
# ---------------------------------------------------------------------------
while [ $# -gt 0 ]; do
  case "$1" in
    --command) COMMAND_NAME="$2"; shift 2 ;;
    --install-dir) INSTALL_DIR="$2"; shift 2 ;;
    --home) GROK_HOME_OPT="$2"; shift 2 ;;
    --bin-dir) BIN_DIR="$2"; shift 2 ;;
    --share-dir) SHARE_DIR="$2"; shift 2 ;;
    --venv-dir) VENV_DIR="$2"; shift 2 ;;
    --repo) REPO_URL="$2"; shift 2 ;;
    --branch) BRANCH="$2"; shift 2 ;;
    --go-version) GO_VERSION="$2"; shift 2 ;;
    --skip-docker) SKIP_DOCKER=1; shift ;;
    --skip-clearance) SKIP_CLEARANCE=1; START_CLEARANCE=0; shift ;;
    --skip-browser) SKIP_BROWSER=1; shift ;;
    --skip-go) SKIP_GO_INSTALL=1; shift ;;
    --no-start-clearance) START_CLEARANCE=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数: $1（--help 查看用法）" ;;
  esac
done

case "$COMMAND_NAME" in
  *[!a-zA-Z0-9._-]*|"") die "非法命令名: $COMMAND_NAME" ;;
esac

# 解析后补全 Linux 默认 GROK_HOME
if [ "$OS" = "linux" ] && [ -z "$GROK_HOME_OPT" ]; then
  if [ "$(id -u)" -eq 0 ]; then
    GROK_HOME_OPT="/root/.grok"
  else
    GROK_HOME_OPT="${HOME:-/root}/.grok"
  fi
fi

# ---------------------------------------------------------------------------
# 公共：写默认 config.env
# ---------------------------------------------------------------------------
write_default_config() {
  local dest="$1"
  cat >"$dest" <<EOF
# 由 install.sh 生成 — 也可用 ${COMMAND_NAME} config 编辑
# 完整说明见: ${GROK_HOME_OPT}/config.env.example

EMAIL_MODE=tempmail

CLEARANCE_ENABLED=1
REGISTER_PROXY=http://127.0.0.1:40080
FLARESOLVERR_URL=http://127.0.0.1:8191
CLEARANCE_PROXY=http://privoxy:8118
CLEARANCE_URLS=https://accounts.x.ai,https://x.ai,https://status.x.ai,https://console.x.ai,https://auth.x.ai

TURNSTILE_PROVIDER=browser

PROTOCOL_HTTP=1
HTTP_POOL_SIZE=8
TEMPMAIL_LOL_RETRIES=30
TEMPMAIL_LOL_MIN_INTERVAL_MS=1500

HTTPS_PROXY=http://127.0.0.1:40080
HTTP_PROXY=http://127.0.0.1:40080
NO_PROXY=127.0.0.1,localhost

PROBE_ENABLED=1

CPA_UPLOAD_ENABLED=0
CPA_MANAGEMENT_BASE=http://127.0.0.1:8317/v0/management
CPA_MANAGEMENT_KEY=
CPA_UPLOAD_TIMEOUT_SEC=30
CPA_UPLOAD_RETRIES=2
CPA_UPLOAD_NAME_TEMPLATE={email}.json
CPA_UPLOAD_VERIFY=1
CPA_UPLOAD_MODE=multipart
EOF
  chmod 600 "$dest" 2>/dev/null || true
}

sync_repo() {
  log "同步源码 → $INSTALL_DIR"
  mkdir -p "$(dirname "$INSTALL_DIR")"
  if [ -d "$INSTALL_DIR/.git" ]; then
    git -C "$INSTALL_DIR" remote set-url origin "$REPO_URL" || true
    git -C "$INSTALL_DIR" fetch origin
    git -C "$INSTALL_DIR" checkout "$BRANCH"
    git -C "$INSTALL_DIR" reset --hard "origin/$BRANCH"
  else
    rm -rf "$INSTALL_DIR"
    git clone --branch "$BRANCH" --depth 1 "$REPO_URL" "$INSTALL_DIR"
  fi
  if [ "$OS" = "linux" ] && [ "$INSTALL_DIR" = "/opt/Grok-Register" ]; then
    ln -sfn "$INSTALL_DIR" /opt/Grok-Reg 2>/dev/null || true
  fi
  ok "源码: $(git -C "$INSTALL_DIR" log -1 --oneline 2>/dev/null || echo ok)"
}

build_and_install_cli() {
  log "编译并安装 CLI → $BIN_DIR/$COMMAND_NAME"
  export PATH="${PATH}:/usr/local/go/bin"
  # Homebrew Go
  if [ "$OS" = "darwin" ] && command -v brew >/dev/null 2>&1; then
    export PATH="$(brew --prefix)/bin:$(brew --prefix)/opt/go/bin:${PATH}"
  fi
  command -v go >/dev/null 2>&1 || die "找不到 go，请先安装 Go 1.21+"
  cd "$INSTALL_DIR"
  mkdir -p bin
  go build -ldflags "-s -w -X main.version=0.1.0" -o "bin/${COMMAND_NAME}" ./cmd/grok
  mkdir -p "$BIN_DIR" "$SHARE_DIR"
  install -m 755 "bin/${COMMAND_NAME}" "${BIN_DIR}/${COMMAND_NAME}"
  install -m 755 scripts/turnstile_mint.py "${SHARE_DIR}/turnstile_mint.py"
  install -m 755 scripts/turnstile_pool.py "${SHARE_DIR}/turnstile_pool.py"
  ok "已安装 ${BIN_DIR}/${COMMAND_NAME}"
  ok "已安装 mint 脚本 → $SHARE_DIR"
}

install_browser() {
  if [ "$SKIP_BROWSER" = 1 ]; then
    warn "已跳过浏览器依赖（Turnstile 将不可用）"
    return 0
  fi
  log "安装 Python venv + Playwright + CloakBrowser → $VENV_DIR"
  command -v python3 >/dev/null 2>&1 || die "找不到 python3"
  python3 -m venv "$VENV_DIR"
  "${VENV_DIR}/bin/pip" install -U pip
  "${VENV_DIR}/bin/pip" install -r "$INSTALL_DIR/scripts/requirements-turnstile.txt"
  # CloakBrowser 装到当前用户 home
  HOME="${HOME:-/root}" "${VENV_DIR}/bin/python" -m cloakbrowser install || \
    "${VENV_DIR}/bin/python" -m cloakbrowser install
  ok "浏览器依赖就绪"
}

start_clearance() {
  if [ "$SKIP_CLEARANCE" = 1 ] || [ "$SKIP_DOCKER" = 1 ] || [ "$START_CLEARANCE" != 1 ]; then
    warn "未启动 clearance（skip / no-start）"
    return 0
  fi
  if ! command -v docker >/dev/null 2>&1; then
    warn "无 docker，跳过 clearance"
    return 0
  fi
  if ! docker info >/dev/null 2>&1; then
    warn "Docker 未运行，跳过 clearance；请启动后执行: cd $INSTALL_DIR/clearance && docker compose up -d"
    return 0
  fi
  log "启动 clearance 清障栈..."
  if [ -f "$INSTALL_DIR/clearance/docker-compose.yml" ]; then
    (cd "$INSTALL_DIR/clearance" && docker compose up -d) || \
      warn "clearance 启动失败，可稍后: cd $INSTALL_DIR/clearance && docker compose up -d"
    (cd "$INSTALL_DIR/clearance" && docker compose ps) || true
  fi
}

prepare_data_dir() {
  log "准备数据目录 $GROK_HOME_OPT"
  mkdir -p "$GROK_HOME_OPT" "$GROK_HOME_OPT/logs" "$GROK_HOME_OPT/outputs"
  chmod 700 "$GROK_HOME_OPT" 2>/dev/null || true

  if [ -f "$INSTALL_DIR/internal/config/example.env" ]; then
    cp -f "$INSTALL_DIR/internal/config/example.env" "$GROK_HOME_OPT/config.env.example"
  elif [ -f "$INSTALL_DIR/config.env.example" ]; then
    cp -f "$INSTALL_DIR/config.env.example" "$GROK_HOME_OPT/config.env.example"
  fi

  if [ ! -f "$GROK_HOME_OPT/config.env" ]; then
    log "写入默认 config.env（EMAIL_MODE=tempmail）"
    write_default_config "$GROK_HOME_OPT/config.env"
  else
    ok "保留已有 config.env"
  fi
}

print_done() {
  local env_hint="$1"
  export GROK_HOME="$GROK_HOME_OPT"
  export GROK_PYTHON="${VENV_DIR}/bin/python"
  export GROK_TURNSTILE_SCRIPT="${SHARE_DIR}/turnstile_mint.py"
  export GROK_TURNSTILE_POOL_SCRIPT="${SHARE_DIR}/turnstile_pool.py"
  export CLOAKBROWSER_SUPPRESS_FONT_WARNING=1

  echo
  echo "=============================================="
  ok "部署完成 ($OS)"
  echo "=============================================="
  echo
  echo "  命令:     ${COMMAND_NAME} help"
  echo "  源码:     ${INSTALL_DIR}"
  echo "  配置:     ${GROK_HOME_OPT}/config.env"
  echo "  示例:     ${GROK_HOME_OPT}/config.env.example"
  echo "  环境:     ${env_hint}"
  echo
  echo "快速开始:"
  echo "  export PATH=\"\$PATH:${BIN_DIR}\""
  echo "  export GROK_HOME=${GROK_HOME_OPT}"
  echo "  export GROK_PYTHON=${VENV_DIR}/bin/python"
  echo "  ${COMMAND_NAME} start"
  echo "  ${COMMAND_NAME} start -t 10 --thread 2"
  echo "  ${COMMAND_NAME} status"
  echo "  ${COMMAND_NAME} logs -f"
  echo "  ${COMMAND_NAME} config"
  echo
  if [ "$COMMAND_NAME" != "grok" ]; then
    echo "提示: 命令名为 ${COMMAND_NAME}（不是 grok）。"
  fi
  if [ "$OS" = "darwin" ]; then
    echo "macOS: 请确认 Docker Desktop 已打开；clearance 异常时:"
  else
    echo "若 clearance 未 healthy:"
  fi
  echo "  cd ${INSTALL_DIR}/clearance && docker compose up -d && docker compose ps"
  echo
  if [ -x "${BIN_DIR}/${COMMAND_NAME}" ]; then
    "${BIN_DIR}/${COMMAND_NAME}" help 2>/dev/null || true
  fi
}

# ===========================================================================
# Linux: Debian/Ubuntu + root
# ===========================================================================
install_linux() {
  if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
      # curl|bash 时 $0 可能是 bash，优先用 BASH_SOURCE
      local self="${BASH_SOURCE[0]:-}"
      if [ -n "$self" ] && [ -f "$self" ]; then
        log "需要 root，通过 sudo 重新执行..."
        exec sudo -E env \
          COMMAND_NAME="$COMMAND_NAME" \
          INSTALL_DIR="$INSTALL_DIR" \
          GROK_HOME="$GROK_HOME_OPT" \
          BIN_DIR="$BIN_DIR" \
          SHARE_DIR="$SHARE_DIR" \
          VENV_DIR="$VENV_DIR" \
          REPO_URL="$REPO_URL" \
          BRANCH="$BRANCH" \
          GO_VERSION="$GO_VERSION" \
          SKIP_DOCKER="$SKIP_DOCKER" \
          SKIP_CLEARANCE="$SKIP_CLEARANCE" \
          SKIP_BROWSER="$SKIP_BROWSER" \
          SKIP_GO_INSTALL="$SKIP_GO_INSTALL" \
          START_CLEARANCE="$START_CLEARANCE" \
          bash "$self" \
          --command "$COMMAND_NAME" \
          --install-dir "$INSTALL_DIR" \
          --home "$GROK_HOME_OPT" \
          --bin-dir "$BIN_DIR" \
          --share-dir "$SHARE_DIR" \
          --venv-dir "$VENV_DIR" \
          --repo "$REPO_URL" \
          --branch "$BRANCH" \
          --go-version "$GO_VERSION" \
          $([ "$SKIP_DOCKER" = 1 ] && echo --skip-docker) \
          $([ "$SKIP_CLEARANCE" = 1 ] && echo --skip-clearance) \
          $([ "$SKIP_BROWSER" = 1 ] && echo --skip-browser) \
          $([ "$SKIP_GO_INSTALL" = 1 ] && echo --skip-go) \
          $([ "$START_CLEARANCE" = 0 ] && echo --no-start-clearance)
      fi
      die "请使用: curl -fsSL .../install.sh | sudo bash"
    fi
    die "请使用 root 或 sudo 运行（Linux）"
  fi

  export DEBIAN_FRONTEND=noninteractive
  export PATH="${PATH}:/usr/local/go/bin"
  export HOME="${HOME:-/root}"

  if [ ! -f /etc/os-release ]; then
    die "仅支持 Debian/Ubuntu（需要 /etc/os-release）"
  fi
  # shellcheck source=/dev/null
  . /etc/os-release
  case "${ID:-}" in
    debian|ubuntu) ;;
    *) warn "未识别发行版 ID=${ID:-?}，将按 Debian/Ubuntu 继续尝试" ;;
  esac

  echo
  echo "=============================================="
  echo " Grok-Register 一键部署 (Linux)"
  echo "=============================================="
  echo "  命令名:     $COMMAND_NAME"
  echo "  源码目录:   $INSTALL_DIR"
  echo "  数据目录:   $GROK_HOME_OPT"
  echo "  二进制:     $BIN_DIR/$COMMAND_NAME"
  echo "  脚本共享:   $SHARE_DIR"
  echo "  Python venv:$VENV_DIR"
  echo "  仓库:       $REPO_URL ($BRANCH)"
  echo "=============================================="
  echo

  log "安装系统依赖..."
  apt-get update -y
  ALSA_PKG=libasound2t64
  if ! apt-cache show libasound2t64 >/dev/null 2>&1; then
    ALSA_PKG=libasound2
  fi
  apt-get install -y --no-install-recommends \
    git curl ca-certificates gnupg lsb-release \
    build-essential make \
    python3 python3-pip python3-venv \
    libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libcups2 \
    libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 \
    libxrandr2 libgbm1 "$ALSA_PKG" libpango-1.0-0 libcairo2 \
    fonts-liberation fonts-noto-cjk \
    || warn "部分包安装失败，可稍后手动补齐"
  ok "系统依赖就绪"

  # Go
  need_go=0
  if ! command -v go >/dev/null 2>&1; then
    need_go=1
  elif ! go version 2>/dev/null | grep -qE 'go1\.(2[1-9]|[3-9][0-9])'; then
    warn "检测到较旧 Go: $(go version 2>/dev/null || true)，将安装 ${GO_VERSION}"
    need_go=1
  fi
  if [ "$need_go" = 1 ]; then
    if [ "$SKIP_GO_INSTALL" = 1 ]; then
      die "系统无可用 Go 1.21+ 且指定了 --skip-go"
    fi
    log "安装 Go ${GO_VERSION} (${GO_ARCH})..."
    tmp="/tmp/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
    curl -fsSL -o "$tmp" "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "$tmp"
    rm -f "$tmp"
    echo 'export PATH=$PATH:/usr/local/go/bin' >/etc/profile.d/go.sh
    export PATH="/usr/local/go/bin:${PATH}"
    ok "Go $(go version)"
  else
    ok "使用已有 Go: $(go version)"
  fi

  # Docker
  if [ "$SKIP_DOCKER" != 1 ]; then
    if ! command -v docker >/dev/null 2>&1; then
      log "安装 Docker..."
      curl -fsSL https://get.docker.com | sh
      systemctl enable --now docker 2>/dev/null || true
    else
      ok "Docker 已存在: $(docker --version 2>/dev/null || true)"
    fi
    if ! docker compose version >/dev/null 2>&1; then
      log "安装 docker compose plugin..."
      apt-get install -y docker-compose-plugin 2>/dev/null || true
    fi
    ok "Docker: $(docker --version 2>/dev/null || echo '?')"
  else
    warn "已跳过 Docker"
  fi

  sync_repo
  build_and_install_cli
  # root 下 CloakBrowser 装到 /root
  HOME=/root install_browser
  start_clearance
  prepare_data_dir

  PROFILE_SNIPPET="/etc/profile.d/grok-register.sh"
  cat >"$PROFILE_SNIPPET" <<EOF
# Grok-Register (generated by install.sh)
export PATH="\$PATH:/usr/local/go/bin:${BIN_DIR}"
export GROK_HOME="${GROK_HOME_OPT}"
export GROK_PYTHON="${VENV_DIR}/bin/python"
export GROK_TURNSTILE_SCRIPT="${SHARE_DIR}/turnstile_mint.py"
export GROK_TURNSTILE_POOL_SCRIPT="${SHARE_DIR}/turnstile_pool.py"
export CLOAKBROWSER_SUPPRESS_FONT_WARNING=1
EOF
  chmod 644 "$PROFILE_SNIPPET"

  if [ -f /root/.bashrc ] && ! grep -q 'GROK_HOME=' /root/.bashrc 2>/dev/null; then
    {
      echo ""
      echo "# Grok-Register"
      echo "export GROK_HOME=\"${GROK_HOME_OPT}\""
      echo "export GROK_PYTHON=\"${VENV_DIR}/bin/python\""
      echo "export GROK_TURNSTILE_SCRIPT=\"${SHARE_DIR}/turnstile_mint.py\""
      echo "export GROK_TURNSTILE_POOL_SCRIPT=\"${SHARE_DIR}/turnstile_pool.py\""
      echo "export CLOAKBROWSER_SUPPRESS_FONT_WARNING=1"
    } >>/root/.bashrc
  fi

  print_done "$PROFILE_SNIPPET"
}

# ===========================================================================
# macOS: 依赖已装 Homebrew + Docker Desktop
# ===========================================================================
install_darwin() {
  # 不强制 root；若误用 sudo，HOME 可能变成 /var/root，给出警告
  if [ "$(id -u)" -eq 0 ]; then
    warn "检测到 root 运行。macOS 建议用普通用户: curl ... | bash（不要 sudo）"
  fi

  export PATH="${PATH}:/usr/local/bin:/opt/homebrew/bin:${HOME:-}/.local/bin"

  echo
  echo "=============================================="
  echo " Grok-Register 一键部署 (macOS)"
  echo "=============================================="
  echo "  命令名:     $COMMAND_NAME"
  echo "  源码目录:   $INSTALL_DIR"
  echo "  数据目录:   $GROK_HOME_OPT"
  echo "  二进制:     $BIN_DIR/$COMMAND_NAME"
  echo "  脚本共享:   $SHARE_DIR"
  echo "  Python venv:$VENV_DIR"
  echo "  仓库:       $REPO_URL ($BRANCH)"
  echo "=============================================="
  echo

  # --- 前置：Homebrew ---
  if ! command -v brew >/dev/null 2>&1; then
    cat >&2 <<'EOM'
[x] 未检测到 Homebrew。

请先安装 Homebrew，然后重新运行本脚本：

  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

安装完成后按提示把 brew 加入 PATH（Apple Silicon 常见）：

  echo 'eval "$(/opt/homebrew/bin/brew shellenv)"' >> ~/.zprofile
  eval "$(/opt/homebrew/bin/brew shellenv)"

文档: https://brew.sh
EOM
    exit 1
  fi
  ok "Homebrew: $(brew --prefix 2>/dev/null || true)"
  eval "$("$(command -v brew)" shellenv)" 2>/dev/null || true
  export PATH="$(brew --prefix)/bin:$(brew --prefix)/sbin:${PATH}"

  # --- 前置：Docker Desktop ---
  if [ "$SKIP_DOCKER" != 1 ]; then
    if ! command -v docker >/dev/null 2>&1; then
      cat >&2 <<'EOM'
[x] 未检测到 docker 命令。

请先安装并启动 Docker Desktop，然后重新运行本脚本：

  brew install --cask docker

  # 或从官网安装:
  # https://www.docker.com/products/docker-desktop/

安装后打开「Docker」应用，等待菜单栏鲸鱼图标就绪，再执行：

  docker info
  curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | bash
EOM
      exit 1
    fi
    if ! docker info >/dev/null 2>&1; then
      cat >&2 <<'EOM'
[x] Docker 已安装但未运行（docker info 失败）。

请打开 Docker Desktop，等待引擎启动后再跑：

  open -a Docker
  # 就绪后:
  docker info
  curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | bash

若暂时不需要清障栈，可跳过检查：

  curl -fsSL .../install.sh | bash -s -- --skip-docker --skip-clearance
EOM
      exit 1
    fi
    ok "Docker: $(docker --version 2>/dev/null || true)"
    docker compose version >/dev/null 2>&1 || warn "docker compose 不可用，清障栈可能无法启动"
  else
    warn "已跳过 Docker 检查"
  fi

  # --- brew 依赖 ---
  log "通过 Homebrew 安装/确认依赖 (git make go python)..."
  local pkgs=()
  command -v git >/dev/null 2>&1 || pkgs+=(git)
  command -v make >/dev/null 2>&1 || pkgs+=(make)
  if [ "$SKIP_GO_INSTALL" != 1 ]; then
    if ! command -v go >/dev/null 2>&1; then
      pkgs+=(go)
    elif ! go version 2>/dev/null | grep -qE 'go1\.(2[1-9]|[3-9][0-9])'; then
      warn "Go 版本偏旧: $(go version 2>/dev/null || true)，将 brew install go"
      pkgs+=(go)
    fi
  fi
  if ! command -v python3 >/dev/null 2>&1; then
    pkgs+=(python)
  fi
  if [ "${#pkgs[@]}" -gt 0 ]; then
    log "brew install ${pkgs[*]}"
    brew install "${pkgs[@]}"
  fi
  if ! command -v go >/dev/null 2>&1; then
    if [ "$SKIP_GO_INSTALL" = 1 ]; then
      die "无 go 且指定了 --skip-go"
    fi
    die "仍找不到 go，请手动: brew install go"
  fi
  ok "Go: $(go version)"
  ok "Python: $(python3 --version 2>/dev/null || true)"

  # 确保 bin 目录在 PATH（~/.local/bin 可能尚未存在）
  mkdir -p "$BIN_DIR" "$SHARE_DIR"

  sync_repo
  build_and_install_cli
  install_browser
  start_clearance
  prepare_data_dir

  # shell 环境：~/.zprofile + ~/.zshrc（mac 默认 zsh）
  local marker="# Grok-Register (generated by install.sh)"
  local block
  block=$(cat <<EOF
${marker}
export PATH="\$PATH:${BIN_DIR}"
export GROK_HOME="${GROK_HOME_OPT}"
export GROK_PYTHON="${VENV_DIR}/bin/python"
export GROK_TURNSTILE_SCRIPT="${SHARE_DIR}/turnstile_mint.py"
export GROK_TURNSTILE_POOL_SCRIPT="${SHARE_DIR}/turnstile_pool.py"
export CLOAKBROWSER_SUPPRESS_FONT_WARNING=1
EOF
)
  local env_hint=""
  for rc in "${HOME}/.zprofile" "${HOME}/.zshrc" "${HOME}/.bash_profile"; do
    case "$rc" in
      *.bash_profile) [ -f "${HOME}/.bashrc" ] || [ -f "$rc" ] || continue ;;
    esac
    touch "$rc"
    if grep -q 'Grok-Register (generated by install.sh)' "$rc" 2>/dev/null; then
      # 粗暴刷新：删旧块再追加（按 marker 到下一空行前较难，直接提示用户 source）
      ok "已存在环境片段: $rc（若路径变更请手动改）"
    else
      printf '\n%s\n' "$block" >>"$rc"
      ok "已写入环境: $rc"
    fi
    env_hint="${env_hint:+$env_hint, }$rc"
  done
  # 至少保证 zprofile
  if [ -z "$env_hint" ]; then
    printf '%s\n' "$block" >>"${HOME}/.zprofile"
    env_hint="${HOME}/.zprofile"
  fi

  # PATH 提示
  case ":$PATH:" in
    *":${BIN_DIR}:"*) ;;
    *) warn "当前 shell 的 PATH 尚无 ${BIN_DIR}，请执行: export PATH=\"\$PATH:${BIN_DIR}\" 或新开终端" ;;
  esac

  print_done "$env_hint"
}

# ---------------------------------------------------------------------------
# 入口
# ---------------------------------------------------------------------------
case "$OS" in
  linux)  install_linux ;;
  darwin) install_darwin ;;
  *) die "内部错误: OS=$OS" ;;
esac
