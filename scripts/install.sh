#!/usr/bin/env bash
# Grok-Register 一键部署
#
# Linux (Debian/Ubuntu，需 root/sudo):
#   curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | sudo bash
#   # 有 TTY 时会询问：命令名 / 安装目录 / 数据目录（回车=默认）
#
# macOS（需已装 Homebrew + Docker Desktop，普通用户即可）:
#   curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | bash
#
# 非交互（CI / 无 TTY）:
#   curl -fsSL ... | sudo NONINTERACTIVE=1 bash
#   curl -fsSL ... | sudo bash -s -- --yes --command grok --install-dir /opt/Grok-Register
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
# 默认值
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
# 0=有 TTY 时询问路径/命令名；1=全默认（CI）
NONINTERACTIVE="${NONINTERACTIVE:-0}"

# 环境变量是否在进脚本前已显式设置（显式则不再交互问该项）
_ENV_COMMAND_NAME="${COMMAND_NAME-}"
_ENV_INSTALL_DIR="${INSTALL_DIR-}"
_ENV_GROK_HOME="${GROK_HOME-}"
_ENV_BIN_DIR="${BIN_DIR-}"
_ENV_SHARE_DIR="${SHARE_DIR-}"
_ENV_VENV_DIR="${VENV_DIR-}"

SET_COMMAND=0
SET_INSTALL_DIR=0
SET_HOME=0
SET_BIN_DIR=0
SET_SHARE_DIR=0
SET_VENV_DIR=0
# 非默认命令名视为用户已指定
[ -n "$_ENV_COMMAND_NAME" ] && [ "$_ENV_COMMAND_NAME" != "grok" ] && SET_COMMAND=1
[ -n "$_ENV_INSTALL_DIR" ] && SET_INSTALL_DIR=1
[ -n "$_ENV_GROK_HOME" ] && SET_HOME=1
[ -n "$_ENV_BIN_DIR" ] && SET_BIN_DIR=1
[ -n "$_ENV_SHARE_DIR" ] && SET_SHARE_DIR=1
[ -n "$_ENV_VENV_DIR" ] && SET_VENV_DIR=1

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

交互:
  终端有 TTY 时会询问命令名 / 安装目录 / 数据目录（直接回车=默认）。
  curl|bash 且无 TTY，或 NONINTERACTIVE=1 / --yes，则全用默认值。

用法:
  install.sh [选项]

选项:
  --command NAME        CLI 命令名（默认 grok）
  --install-dir PATH    源码目录
  --home PATH           数据目录 GROK_HOME
  --bin-dir PATH        二进制目录
  --share-dir PATH      mint 脚本目录
  --venv-dir PATH       Python venv 路径
  --repo URL            Git 仓库
  --branch NAME         分支（默认 main）
  --go-version VER      Linux 官方 tarball Go 版本
  --skip-docker         不安装/不检查 Docker
  --skip-clearance      不起 clearance
  --skip-browser        不装 Playwright/CloakBrowser
  --skip-go             不自动安装 Go
  --no-start-clearance  不 docker compose up
  --yes / -y            非交互，全部默认
  -h, --help            帮助

示例:
  curl -fsSL .../install.sh | sudo bash
  curl -fsSL .../install.sh | sudo bash -s -- --command grok-reg --home /home/ubuntu/.grok
  curl -fsSL .../install.sh | sudo NONINTERACTIVE=1 bash
  # macOS
  curl -fsSL .../install.sh | bash
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
    --command) COMMAND_NAME="$2"; SET_COMMAND=1; shift 2 ;;
    --install-dir) INSTALL_DIR="$2"; SET_INSTALL_DIR=1; shift 2 ;;
    --home) GROK_HOME_OPT="$2"; SET_HOME=1; shift 2 ;;
    --bin-dir) BIN_DIR="$2"; SET_BIN_DIR=1; shift 2 ;;
    --share-dir) SHARE_DIR="$2"; SET_SHARE_DIR=1; shift 2 ;;
    --venv-dir) VENV_DIR="$2"; SET_VENV_DIR=1; shift 2 ;;
    --repo) REPO_URL="$2"; shift 2 ;;
    --branch) BRANCH="$2"; shift 2 ;;
    --go-version) GO_VERSION="$2"; shift 2 ;;
    --skip-docker) SKIP_DOCKER=1; shift ;;
    --skip-clearance) SKIP_CLEARANCE=1; START_CLEARANCE=0; shift ;;
    --skip-browser) SKIP_BROWSER=1; shift ;;
    --skip-go) SKIP_GO_INSTALL=1; shift ;;
    --no-start-clearance) START_CLEARANCE=0; shift ;;
    --yes|-y) NONINTERACTIVE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数: $1（--help 查看用法）" ;;
  esac
done

case "$COMMAND_NAME" in
  *[!a-zA-Z0-9._-]*|"") die "非法命令名: $COMMAND_NAME" ;;
esac

# ---------------------------------------------------------------------------
# 真实调用用户（sudo 时用 SUDO_USER，避免 GROK_HOME 落到 /root）
# ---------------------------------------------------------------------------
REAL_USER="${SUDO_USER:-$(id -un)}"
REAL_HOME="${HOME:-}"
if [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
  if command -v getent >/dev/null 2>&1; then
    REAL_HOME="$(getent passwd "$SUDO_USER" | cut -d: -f6)"
  fi
  [ -z "$REAL_HOME" ] && REAL_HOME="/home/$SUDO_USER"
elif [ -z "$REAL_HOME" ]; then
  REAL_HOME="$(eval echo "~$REAL_USER" 2>/dev/null || echo "/root")"
fi

# Linux 默认 GROK_HOME：优先真实用户 home
if [ "$OS" = "linux" ] && [ "$SET_HOME" != 1 ] && [ -z "$GROK_HOME_OPT" ]; then
  if [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
    GROK_HOME_OPT="${REAL_HOME}/.grok"
  elif [ "$(id -u)" -eq 0 ]; then
    GROK_HOME_OPT="/root/.grok"
  else
    GROK_HOME_OPT="${HOME:-$REAL_HOME}/.grok"
  fi
fi

# ---------------------------------------------------------------------------
# 交互询问（/dev/tty，兼容 curl|sudo bash）
# ---------------------------------------------------------------------------
prompt_value() {
  # prompt_value VAR "说明" "默认"
  local __var="$1" __msg="$2" __def="$3" __ans=""
  if [ ! -r /dev/tty ] || [ ! -w /dev/tty ]; then
    printf -v "$__var" '%s' "$__def"
    return 0
  fi
  printf '%s [%s]: ' "$__msg" "$__def" >/dev/tty
  IFS= read -r __ans </dev/tty || true
  if [ -z "${__ans}" ]; then
    __ans="$__def"
  fi
  printf -v "$__var" '%s' "$__ans"
}

maybe_prompt_paths() {
  if [ "$NONINTERACTIVE" = 1 ]; then
    log "非交互模式：使用默认/参数路径"
    return 0
  fi
  if [ ! -r /dev/tty ] || [ ! -w /dev/tty ]; then
    warn "无 TTY（常见于 curl|bash 无伪终端），使用默认路径；可用 --command / --install-dir / --home 覆盖"
    return 0
  fi

  echo >/dev/tty
  echo "==============================================" >/dev/tty
  echo " 安装选项（直接回车 = 使用方括号内默认值）" >/dev/tty
  echo "==============================================" >/dev/tty

  if [ "$SET_COMMAND" != 1 ]; then
    prompt_value COMMAND_NAME "CLI 命令名" "$COMMAND_NAME"
    case "$COMMAND_NAME" in
      *[!a-zA-Z0-9._-]*|"") die "非法命令名: $COMMAND_NAME" ;;
    esac
  fi
  if [ "$SET_INSTALL_DIR" != 1 ]; then
    prompt_value INSTALL_DIR "源码安装目录" "$INSTALL_DIR"
  fi
  if [ "$SET_HOME" != 1 ]; then
    prompt_value GROK_HOME_OPT "数据目录 GROK_HOME" "$GROK_HOME_OPT"
  fi
  if [ "$SET_BIN_DIR" != 1 ]; then
    prompt_value BIN_DIR "二进制目录" "$BIN_DIR"
  fi
  if [ "$SET_VENV_DIR" != 1 ]; then
    prompt_value VENV_DIR "Python venv 目录" "$VENV_DIR"
  fi
  # share 默认跟 bin 同 PREFIX 语义：Linux /usr/local/share；已设则跳过
  if [ "$SET_SHARE_DIR" != 1 ] && [ "$OS" = "linux" ]; then
    :
  fi

  echo >/dev/tty
  ok "将使用:"
  echo "  命令:   $COMMAND_NAME" >/dev/tty
  echo "  源码:   $INSTALL_DIR" >/dev/tty
  echo "  数据:   $GROK_HOME_OPT" >/dev/tty
  echo "  二进制: $BIN_DIR/$COMMAND_NAME" >/dev/tty
  echo "  venv:   $VENV_DIR" >/dev/tty
  echo >/dev/tty
}

maybe_prompt_paths

# ---------------------------------------------------------------------------
# 公共：从仓库 example 种子化 config.env（分区 + 中文注释）
# ---------------------------------------------------------------------------
seed_config_from_example() {
  local dest="$1"
  local example=""
  if [ -f "$INSTALL_DIR/internal/config/example.env" ]; then
    example="$INSTALL_DIR/internal/config/example.env"
  elif [ -f "$INSTALL_DIR/config.env.example" ]; then
    example="$INSTALL_DIR/config.env.example"
  fi
  if [ -z "$example" ]; then
    warn "找不到 example 模板，写精简 config.env"
    cat >"$dest" <<EOF
# 由 install.sh 生成 — ${COMMAND_NAME} config 可编辑
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
    return 0
  fi
  # 复制完整分区中文模板，并统一 CPA 默认 host
  cp -f "$example" "$dest"
  # 确保 CPA_MANAGEMENT_BASE / KEY 存在
  if ! grep -qE '^CPA_MANAGEMENT_BASE=' "$dest" 2>/dev/null; then
    printf '\nCPA_MANAGEMENT_BASE=http://127.0.0.1:8317/v0/management\n' >>"$dest"
  else
    # localhost → 127.0.0.1（与文档一致）
    sed -i.bak 's#^CPA_MANAGEMENT_BASE=http://localhost:#CPA_MANAGEMENT_BASE=http://127.0.0.1:#' "$dest" 2>/dev/null || \
      sed -i '' 's#^CPA_MANAGEMENT_BASE=http://localhost:#CPA_MANAGEMENT_BASE=http://127.0.0.1:#' "$dest" 2>/dev/null || true
    rm -f "${dest}.bak" 2>/dev/null || true
  fi
  if ! grep -qE '^CPA_MANAGEMENT_KEY=' "$dest" 2>/dev/null; then
    # 模板里已有 KEY= 行
    if ! grep -q 'CPA_MANAGEMENT_KEY=' "$dest" 2>/dev/null; then
      printf 'CPA_MANAGEMENT_KEY=\n' >>"$dest"
    fi
  fi
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
  local cb_home="${HOME:-/root}"
  # sudo 安装时浏览器装到真实用户 home，避免 root-only
  if [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
    cb_home="$REAL_HOME"
  fi
  HOME="$cb_home" "${VENV_DIR}/bin/python" -m cloakbrowser install || \
    HOME="$cb_home" "${VENV_DIR}/bin/python" -m cloakbrowser install
  ok "浏览器依赖就绪 (CloakBrowser home=$cb_home)"
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
    warn "Docker 未运行，跳过 clearance；请启动后: cd $INSTALL_DIR/clearance && docker compose up -d"
    return 0
  fi
  log "启动 clearance 清障栈..."
  if [ -f "$INSTALL_DIR/clearance/docker-compose.yml" ]; then
    (cd "$INSTALL_DIR/clearance" && docker compose up -d) || \
      warn "clearance 启动失败，可稍后: cd $INSTALL_DIR/clearance && docker compose up -d"
    (cd "$INSTALL_DIR/clearance" && docker compose ps) || true
  fi
}

chown_if_sudo_user() {
  # 数据/venv 属主改回 SUDO_USER，便于普通用户 grok start
  if [ "$(id -u)" -eq 0 ] && [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
    local grp
    grp="$(id -gn "$SUDO_USER" 2>/dev/null || echo "$SUDO_USER")"
    chown -R "${SUDO_USER}:${grp}" "$GROK_HOME_OPT" 2>/dev/null || true
    # venv 若在 /opt 保持 root 可执行即可；若在用户目录则 chown
    case "$VENV_DIR" in
      /home/*|"$REAL_HOME"/*) chown -R "${SUDO_USER}:${grp}" "$VENV_DIR" 2>/dev/null || true ;;
    esac
    case "$INSTALL_DIR" in
      /home/*|"$REAL_HOME"/*) chown -R "${SUDO_USER}:${grp}" "$INSTALL_DIR" 2>/dev/null || true ;;
    esac
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
    log "写入分区中文 config.env（完整模板）"
    seed_config_from_example "$GROK_HOME_OPT/config.env"
  else
    ok "保留已有 config.env"
  fi
  chown_if_sudo_user
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
  if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
    echo
    echo "  注意: 请用用户 ${SUDO_USER} 运行（不要长期 root）："
    echo "    export GROK_HOME=${GROK_HOME_OPT}"
    echo "    export GROK_PYTHON=${VENV_DIR}/bin/python"
  fi
  echo
  echo "快速开始:"
  echo "  export PATH=\"\$PATH:${BIN_DIR}\""
  echo "  export GROK_HOME=${GROK_HOME_OPT}"
  echo "  export GROK_PYTHON=${VENV_DIR}/bin/python"
  echo "  ${COMMAND_NAME} config              # 编辑配置（分区中文）"
  echo "  ${COMMAND_NAME} start"
  echo "  ${COMMAND_NAME} start -t 1 --thread 1   # 低配机请用 1 线程"
  echo "  ${COMMAND_NAME} status"
  echo "  ${COMMAND_NAME} logs -f"
  echo
  if [ "$COMMAND_NAME" != "grok" ]; then
    echo "提示: 命令名为 ${COMMAND_NAME}（不是 grok）。"
  fi
  echo "硬件提示: 清障栈+1 浏览器约需 2GB+ RAM；1GB 机器务必 --thread 1 且保证 swap。"
  echo "clearance: cd ${INSTALL_DIR}/clearance && docker compose up -d && docker compose ps"
  echo
  if [ -x "${BIN_DIR}/${COMMAND_NAME}" ]; then
    "${BIN_DIR}/${COMMAND_NAME}" help 2>/dev/null || true
  fi
}

# ===========================================================================
# Linux
# ===========================================================================
install_linux() {
  if [ "$(id -u)" -ne 0 ]; then
    if command -v sudo >/dev/null 2>&1; then
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
          NONINTERACTIVE="$NONINTERACTIVE" \
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
          $([ "$START_CLEARANCE" = 0 ] && echo --no-start-clearance) \
          $([ "$NONINTERACTIVE" = 1 ] && echo --yes)
      fi
      die "请使用: curl -fsSL .../install.sh | sudo bash"
    fi
    die "请使用 root 或 sudo 运行（Linux）"
  fi

  export DEBIAN_FRONTEND=noninteractive
  export PATH="${PATH}:/usr/local/go/bin"
  # cloakbrowser 等：优先真实用户 home
  if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
    export HOME="$REAL_HOME"
  else
    export HOME="${HOME:-/root}"
  fi

  if [ ! -f /etc/os-release ]; then
    die "仅支持 Debian/Ubuntu（需要 /etc/os-release）"
  fi
  # shellcheck source=/dev/null
  . /etc/os-release
  case "${ID:-}" in
    debian|ubuntu) ;;
    *) warn "未识别发行版 ID=${ID:-?}，将按 Debian/Ubuntu 继续尝试" ;;
  esac

  # 低配内存提示
  if [ -r /proc/meminfo ]; then
    local mem_kb
    mem_kb="$(awk '/MemTotal/ {print $2}' /proc/meminfo 2>/dev/null || echo 0)"
    if [ "${mem_kb:-0}" -gt 0 ] && [ "$mem_kb" -lt 1800000 ]; then
      warn "检测到内存约 $((mem_kb/1024))MiB：建议 ≥2GiB 才能顺畅跑浏览器+清障"
      warn "请始终: ${COMMAND_NAME} start -t N --thread 1 ，并确保有 2G+ swap"
    fi
  fi

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
  echo "  运行用户:   ${SUDO_USER:-root}"
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
    # 允许 SUDO_USER 用 docker
    if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
      usermod -aG docker "$SUDO_USER" 2>/dev/null || true
    fi
    ok "Docker: $(docker --version 2>/dev/null || echo '?')"
  else
    warn "已跳过 Docker"
  fi

  sync_repo
  build_and_install_cli
  install_browser
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

  # 写入真实用户 bashrc / zshrc
  if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
    for rc in "${REAL_HOME}/.bashrc" "${REAL_HOME}/.zshrc" "${REAL_HOME}/.profile"; do
      [ -d "$(dirname "$rc")" ] || continue
      touch "$rc" 2>/dev/null || continue
      if ! grep -q 'Grok-Register (generated by install.sh)' "$rc" 2>/dev/null; then
        {
          echo ""
          echo "# Grok-Register (generated by install.sh)"
          echo "export GROK_HOME=\"${GROK_HOME_OPT}\""
          echo "export GROK_PYTHON=\"${VENV_DIR}/bin/python\""
          echo "export GROK_TURNSTILE_SCRIPT=\"${SHARE_DIR}/turnstile_mint.py\""
          echo "export GROK_TURNSTILE_POOL_SCRIPT=\"${SHARE_DIR}/turnstile_pool.py\""
          echo "export CLOAKBROWSER_SUPPRESS_FONT_WARNING=1"
          echo "export PATH=\"\$PATH:${BIN_DIR}\""
        } >>"$rc"
        chown "${SUDO_USER}:$(id -gn "$SUDO_USER" 2>/dev/null || echo "$SUDO_USER")" "$rc" 2>/dev/null || true
        ok "已写入环境: $rc"
      fi
    done
  elif [ -f /root/.bashrc ] && ! grep -q 'GROK_HOME=' /root/.bashrc 2>/dev/null; then
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
# macOS
# ===========================================================================
install_darwin() {
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

  if ! command -v brew >/dev/null 2>&1; then
    cat >&2 <<'EOM'
[x] 未检测到 Homebrew。

请先安装 Homebrew，然后重新运行本脚本：

  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

  echo 'eval "$(/opt/homebrew/bin/brew shellenv)"' >> ~/.zprofile
  eval "$(/opt/homebrew/bin/brew shellenv)"

文档: https://brew.sh
EOM
    exit 1
  fi
  ok "Homebrew: $(brew --prefix 2>/dev/null || true)"
  eval "$("$(command -v brew)" shellenv)" 2>/dev/null || true
  export PATH="$(brew --prefix)/bin:$(brew --prefix)/sbin:${PATH}"

  if [ "$SKIP_DOCKER" != 1 ]; then
    if ! command -v docker >/dev/null 2>&1; then
      cat >&2 <<'EOM'
[x] 未检测到 docker 命令。

  brew install --cask docker
  # 或 https://www.docker.com/products/docker-desktop/
  open -a Docker
  docker info
  curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | bash
EOM
      exit 1
    fi
    if ! docker info >/dev/null 2>&1; then
      cat >&2 <<'EOM'
[x] Docker 已安装但未运行。

  open -a Docker
  # 就绪后重跑 install.sh
  # 或: bash -s -- --skip-docker --skip-clearance
EOM
      exit 1
    fi
    ok "Docker: $(docker --version 2>/dev/null || true)"
  else
    warn "已跳过 Docker 检查"
  fi

  log "通过 Homebrew 安装/确认依赖 (git make go python)..."
  local pkgs=()
  command -v git >/dev/null 2>&1 || pkgs+=(git)
  command -v make >/dev/null 2>&1 || pkgs+=(make)
  if [ "$SKIP_GO_INSTALL" != 1 ]; then
    if ! command -v go >/dev/null 2>&1; then
      pkgs+=(go)
    elif ! go version 2>/dev/null | grep -qE 'go1\.(2[1-9]|[3-9][0-9])'; then
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
  command -v go >/dev/null 2>&1 || die "仍找不到 go，请: brew install go"
  ok "Go: $(go version)"
  ok "Python: $(python3 --version 2>/dev/null || true)"

  mkdir -p "$BIN_DIR" "$SHARE_DIR"
  sync_repo
  build_and_install_cli
  install_browser
  start_clearance
  prepare_data_dir

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
    touch "$rc" 2>/dev/null || continue
    if grep -q 'Grok-Register (generated by install.sh)' "$rc" 2>/dev/null; then
      ok "已存在环境片段: $rc"
    else
      printf '\n%s\n' "$block" >>"$rc"
      ok "已写入环境: $rc"
    fi
    env_hint="${env_hint:+$env_hint, }$rc"
  done
  [ -n "$env_hint" ] || env_hint="${HOME}/.zprofile"

  print_done "$env_hint"
}

case "$OS" in
  linux)  install_linux ;;
  darwin) install_darwin ;;
  *) die "内部错误: OS=$OS" ;;
esac
