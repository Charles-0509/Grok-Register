# Grok-Register

Grok 免费号 **注册 → OAuth → CPA 可用 JSON** 二合一 CLI（Go）。

一条命令后台跑完，产物可直接导入 CPA / cliproxy 类网关。

```bash
grok start                 # 交互：数量 + 线程(1–8)
grok start -t 10 --thread 2
grok status
grok logs -f
grok stop
grok config                # 编辑 ~/.grok/config.env
grok upload                # 手动上传 CPA JSON 到 Management API
```

---

## 近期特性

| 特性 | 说明 |
|------|------|
| **协议优先 + TLS 指纹** | Go `tls-client`（默认 `chrome_131`）过 CF；`CLEARANCE_MODE=auto` 失败再拉 WARP/FS |
| **Turnstile 屏外有头** | 默认 `TURNSTILE_MODE=offscreen`（非真 headless，降低 600010） |
| **Castle 空 token** | 当前 `castleRequestToken=""`；风控收紧后再补 offline |
| **testmail** | `EMAIL_MODE=testmail`，GitHub Student Pack 等 |
| **全局座位上限** | `done + reserved ≤ target` |
| **CPA 上传 wait** | 结束前等待 Management 上传，避免进程先退出 |
| **一键安装** | 路径/命令名/WARP/结束停容器交互；安全同步（不再误删非空目录） |
| **`grok stop` 停清障** | `CLEARANCE_AUTO_STOP=1` 时手动 stop 也会 `docker compose stop` |
| **grok2api 导出** | `outputs/<run>/grok2api/tokens.txt` 仅 SSO token（一行一个） |
| **OAuth 限速** | 全局间隔 + discovery 缓存 + rate_limited 重试 |

### 架构三腿

```text
协议腿  gRPC 发/验码 → Server Action → SSO hop → OAuth → 探活/CPA
边缘腿  chrome_131 TLS 优先 → CF 拦再 clearance（auto）
挑战腿  Turnstile 仅 Chromium（offscreen 池 / one-shot）
```

冒烟（不注册账号）:

```bash
go run scripts/smoke_protocol.go
REGISTER_PROXY=http://127.0.0.1:7890 go run scripts/smoke_protocol.go
```

---

## 一键安装（推荐）

`scripts/install.sh` 自动识别平台：

| 平台 | 前提 | 默认安装位置 |
|------|------|----------------|
| **Linux**（Debian/Ubuntu） | root / sudo | 源码 `/opt/Grok-Register`；数据优先 **`SUDO_USER` 的 `~/.grok`**（非 `/root`） |
| **macOS** | 已装 **Homebrew** + **Docker Desktop**（缺则提示安装命令后退出） | `~/Grok-Register`，数据 `~/.grok`，CLI `~/.local/bin` |

会拉源码、编译 CLI、装 Playwright/CloakBrowser、起 clearance，并写入**分区中文注释**的 `config.env`（与 `config.env.example` 同模板）。

### 交互询问

有真实 TTY 时会依次提示：

1. CLI 命令名 / 源码目录 / 数据目录 / 二进制 / venv  
2. **是否启用 WARP 清障栈？** `[Y]`  
   - **Y（默认）**：起 Docker 清障，`REGISTER_PROXY=http://127.0.0.1:40080`  
   - **N**：不装清障；再问 **本机 HTTP 代理端口**  
     - 输入如 `7890` → `REGISTER_PROXY=http://127.0.0.1:7890`，`CLEARANCE_ENABLED=0`  
     - **直接回车** → 直连（无代理，适合能访问 x.ai 的境外 VPS）  
3. **（WARP 时）运行结束后是否自动关闭清障容器？** `[Y]`  
   - **Y（默认）**：`CLEARANCE_AUTO_STOP=1`，结束/中断后 `docker compose stop`  
   - **N**：容器常开；每次 `grok start` 仍会检测并自动拉起未运行的栈
无 TTY 的 `curl|sudo bash` 可能无法提问，此时：

```bash
# WARP 清障（默认）
curl -fsSL .../install.sh | sudo bash -s -- --with-warp

# 本机 Clash 等代理
curl -fsSL .../install.sh | sudo bash -s -- --no-warp --proxy-port 7890

# 境外 VPS 直连
curl -fsSL .../install.sh | sudo bash -s -- --no-warp

# 强制全默认（WARP）
curl -fsSL .../install.sh | sudo NONINTERACTIVE=1 bash
```

### Linux 一行

```bash
curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | sudo bash
```

| 项 | 默认 |
|----|------|
| 命令 | `/usr/local/bin/grok` |
| 源码 | `/opt/Grok-Register`（软链 `/opt/Grok-Reg`） |
| 数据 | `sudo` 时为 **`/home/<SUDO_USER>/.grok`**，纯 root 为 `/root/.grok` |
| Python | `/opt/cloakbrowser-venv/bin/python` |
| mint | `/usr/local/share/grok-reg/turnstile_{mint,pool}.py` |
### macOS 一行

**先**确认：

```bash
# 1) Homebrew — 若无:
# /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"

# 2) Docker Desktop — 若无:
# brew install --cask docker
# 打开 Docker 应用，等引擎就绪: docker info
```

然后（**不要 sudo**）：

```bash
curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | bash
```

| 项 | 默认 |
|----|------|
| 命令 | `~/.local/bin/grok` |
| 源码 | `~/Grok-Register` |
| 数据 | `~/.grok` |
| Python | `~/.local/share/cloakbrowser-venv/bin/python` |
| mint | `~/.local/share/grok-reg/turnstile_{mint,pool}.py` |
| 环境 | 写入 `~/.zprofile` / `~/.zshrc` |

缺 brew 或 Docker 时脚本会打印安装命令并退出，装好后重跑同一行即可。

### 自定义命令名 / 目录

```bash
# Linux：改命令名
curl -fsSL .../install.sh | sudo bash -s -- --command grok-reg

# Linux：自定义目录
curl -fsSL .../install.sh | sudo bash -s -- \
  --install-dir /data/Grok-Register --home /data/grok-data

# macOS：改命令名 / 把二进制装到 brew 前缀
curl -fsSL .../install.sh | bash -s -- --command grok-reg
curl -fsSL .../install.sh | bash -s -- --bin-dir "$(brew --prefix)/bin"
```

### 常用选项

| 选项 / 环境变量 | Linux 默认 | macOS 默认 | 说明 |
|-----------------|------------|------------|------|
| `--command` | `grok` | 同左 | CLI 命令名 |
| `--install-dir` | `/opt/Grok-Register` | `~/Grok-Register` | 源码 |
| `--home` | `/root/.grok` | `~/.grok` | 数据 |
| `--bin-dir` | `/usr/local/bin` | `~/.local/bin` | 二进制 |
| `--share-dir` | `/usr/local/share/grok-reg` | `~/.local/share/grok-reg` | mint 脚本 |
| `--venv-dir` | `/opt/cloakbrowser-venv` | `~/.local/share/cloakbrowser-venv` | Python venv |
| `--skip-docker` | off | off | 不检查/不装 Docker |
| `--skip-clearance` | off | off | 不起清障 |
| `--skip-browser` | off | off | 不装 Playwright |
| `--skip-go` | off | off | 不自动装 Go |
| `--no-start-clearance` | off | off | 不 `compose up` |

本地已 clone：

```bash
# Linux
sudo bash scripts/install.sh --command grok-reg
# macOS
bash scripts/install.sh --command grok-reg
# 或仅装二进制
make build && sudo make install          # Linux
make build && make install PREFIX="$HOME/.local" APP=grok
```

### 装完立刻跑

```bash
# Linux
export GROK_HOME=/root/.grok
export GROK_PYTHON=/opt/cloakbrowser-venv/bin/python

# macOS（或新开终端，已写进 zprofile）
export PATH="$PATH:$HOME/.local/bin"
export GROK_HOME="$HOME/.grok"
export GROK_PYTHON="$HOME/.local/share/cloakbrowser-venv/bin/python"

grok start
grok status
grok logs -f
```

clearance：

```bash
# Linux
cd /opt/Grok-Register/clearance && docker compose up -d && docker compose ps
# macOS
cd ~/Grok-Register/clearance && docker compose up -d && docker compose ps
```

---

## 系统要求

| 组件 | 用途 | 不装会怎样 |
|------|------|------------|
| Go 1.21+ | 仅编译 CLI | 无法 build |
| Python 3.10+ + venv | Turnstile Playwright mint | 拿不到 token |
| Playwright + CloakBrowser | 无头过 CF Turnstile | `timeout` / `iframes=0` |
| Docker | 清障栈（强烈推荐） | 注册/邮箱/CF 更容易挂 |
| CPA Management（可选） | `grok upload` / 自动上传 | 本地仍有 `CPA/*.json` |

### 推荐硬件（运行时，非编译）

| 场景 | 内存 | CPU | 说明 |
|------|------|-----|------|
| **最低能跑** | **2 GiB** + 2 GiB swap | 1–2 vCPU | 仅 `--thread 1`；清障 + 1 个 Chromium |
| **舒适** | **4 GiB** | 2–4 vCPU | `--thread 1~2` |
| **冲量** | **8 GiB+** | 4+ vCPU | `--thread 3~4`（再高收益有限） |

粗算占用（`start -t 1 --thread 1`）：

| 组件 | 约占用 |
|------|--------|
| WARP + Privoxy + FlareSolverr | 400–900 MiB |
| CloakBrowser / Chromium（1 个） | 300–800 MiB |
| grok CLI + Python mint | 50–150 MiB |
| 系统 / Docker 开销 | 200–400 MiB |
| **合计** | **约 1.2–2.5 GiB** 峰值 |

**≤1 GiB 内存的机器会非常卡**（大量 swap）：第一次 `start` 还要冷启动容器镜像层 + 拉起浏览器，更慢。优化：

1. 始终 `--thread 1`（低配禁止 2+）  
2. 保证 **≥2 GiB swap**（你机上已有 4G swap 是对的）  
3. 装完后先让 clearance `healthy` 再 start，避免并行拉镜像  
4. 不要同时跑其它重服务（面板、多开 Docker）  
5. 可选：不需要自动清障预热时关 `CLEARANCE_ENABLED=0`（成功率可能下降）
---

## 完整部署（手动分步）

> 一键脚本失败或需精细控制时使用。目标：系统依赖 → Go → Docker → 编译 → 无头浏览器 → 清障 → 配置 → 跑注册。

### 0. 系统依赖

```bash
sudo apt update
sudo apt install -y \
  git curl ca-certificates gnupg lsb-release \
  build-essential make \
  python3 python3-pip python3-venv \
  libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libcups2 \
  libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 \
  libxrandr2 libgbm1 libasound2t64 libpango-1.0-0 libcairo2 \
  fonts-liberation fonts-noto-cjk
```

> 若 `libasound2t64` 不存在，改成 `libasound2`。

### 1. 安装 Go（仅编译需要，建议 1.21+）

```bash
cd /tmp
curl -fsSL -o go.tgz https://go.dev/dl/go1.24.4.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go.tgz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
export PATH=$PATH:/usr/local/go/bin
go version
```

### 2. 安装 Docker（清障栈用）

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo systemctl enable --now docker
docker compose version || sudo apt install -y docker-compose-plugin
```

### 3. 拉取并编译安装

```bash
sudo mkdir -p /opt
cd /opt
sudo git clone https://github.com/Charles-0509/Grok-Register.git
cd /opt/Grok-Register

export PATH=$PATH:/usr/local/go/bin
make build
sudo make install
# 自定义命令名：
# make build APP=grok-reg && sudo make install APP=grok-reg

grok help
```

`sudo make install` 在已有 `bin/grok` 时**不会**再调 `go`（避免 root PATH 里没有 go）。

### 4. 无头浏览器：Playwright + CloakBrowser（**必做**）

```bash
sudo python3 -m venv /opt/cloakbrowser-venv
sudo /opt/cloakbrowser-venv/bin/pip install -U pip
sudo /opt/cloakbrowser-venv/bin/pip install -r /opt/Grok-Register/scripts/requirements-turnstile.txt
sudo /opt/cloakbrowser-venv/bin/python -m cloakbrowser install

echo 'export GROK_PYTHON=/opt/cloakbrowser-venv/bin/python' | sudo tee -a /root/.bashrc
echo 'export CLOAKBROWSER_SUPPRESS_FONT_WARNING=1' | sudo tee -a /root/.bashrc
export GROK_PYTHON=/opt/cloakbrowser-venv/bin/python
export CLOAKBROWSER_SUPPRESS_FONT_WARNING=1
```

**冒烟测试**（清障栈起来后）：

```bash
export GROK_PYTHON=/opt/cloakbrowser-venv/bin/python
$GROK_PYTHON /usr/local/share/grok-reg/turnstile_mint.py \
  --site-key 0x4AAAAAAAhr9JGVDZbrZOo0 \
  --url https://accounts.x.ai/sign-up \
  --proxy http://127.0.0.1:40080 \
  --timeout 70
echo exit:$?
```

### 5. 清障栈

```bash
cd /opt/Grok-Register/clearance
sudo docker compose up -d
sudo docker compose ps
```

| 端口 | 服务 |
|------|------|
| `127.0.0.1:40000` | WARP SOCKS5 |
| `127.0.0.1:40080` | Privoxy HTTP |
| `127.0.0.1:8191` | FlareSolverr |

### 6. 配置 `~/.grok/config.env`

```bash
sudo mkdir -p /root/.grok
# 也可：grok config（首次会生成 example + 可编辑）
sudo tee /root/.grok/config.env >/dev/null <<'EOF'
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
EOF
```

邮箱模式：

```env
# 1) 公共临时邮箱（默认，无需 token）
EMAIL_MODE=tempmail

# 2) testmail.app
# EMAIL_MODE=testmail
# TESTMAIL_API_KEY=你的_apikey
# TESTMAIL_NAMESPACE=你的_namespace
# TESTMAIL_DOMAIN=inbox.testmail.app

# 3) 自建域名
# EMAIL_MODE=custom
# EMAIL_DOMAIN=example.com
# EMAIL_API=http://127.0.0.1:8080
```

`tempmail` = tempmail.lol + mail.tm 系 fallback，**无需私人 Token**。  
`testmail` 密钥只写本地 `config.env`，勿提交仓库。

### 7. 启动与运维

```bash
export GROK_PYTHON=/opt/cloakbrowser-venv/bin/python
export CLOAKBROWSER_SUPPRESS_FONT_WARNING=1

grok start
grok start -t 10 --thread 3
grok status
grok logs -f
grok stop
grok config
grok upload
```

**数据目录**（`GROK_HOME`，默认 `~/.grok`）：

```text
~/.grok/
├── config.env / config.env.example
├── run.pid / run.lock / state.json
├── logs/run-yyyymmdd-HHMMSS.log
└── outputs/<run_id>/
    ├── SSO/                 # email:password:sso
    ├── CPA/                 # 探活成功 JSON（cliproxy/CPA）
    ├── grok2api/tokens.txt  # 仅 SSO token，一行一个
    └── discarded/
```

### 8. 更新

```bash
# 推荐：重跑一键（保留 config.env，自动补齐新键）
curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | sudo bash

# 或手动
cd /opt/Grok-Register   # mac: ~/Grok-Register
sudo git pull
export PATH=$PATH:/usr/local/go/bin
make build && sudo make install   # mac: make install PREFIX=$HOME/.local
# Linux 无头服务器 Turnstile:
sudo apt-get install -y xvfb
```

### macOS 备注

- **推荐一键**：先装 Homebrew + Docker Desktop，再 `curl .../install.sh | bash`（见上文）  
- 手动：`brew install go python`，venv + CloakBrowser，`make build && make install PREFIX=$HOME/.local`  
- 清障：打开 Docker Desktop 后 `cd ~/Grok-Register/clearance && docker compose up -d`
---

## 命令一览

| 命令 | 说明 |
|------|------|
| `grok start` | 交互：注册数量 + 并发线程(1–8) |
| `grok start -t N --thread M` | 目标 N（1–10000）；线程 M（1–8）；**计数 = CPA 探活成功数** |
| `grok status` | 进度、线程、当前步骤 |
| `grok logs` / `logs -f` | 最近日志 / 跟踪 |
| `grok stop` | 停止注册机；`CLEARANCE_AUTO_STOP=1` 时同步停清障容器 |
| `grok config` | 打开 `config.env`，刷新 `config.env.example` |
| `grok upload` | 选最近 run，上传 CPA JSON |

---

## 配置补充

| 变量 | 说明 |
|------|------|
| `GROK_HOME` | 数据根，默认 `~/.grok` |
| `GROK_PYTHON` | mint/pool 用的 Python |
| `GROK_TURNSTILE_SCRIPT` | one-shot mint 路径 |
| `GROK_TURNSTILE_POOL_SCRIPT` | 常驻池路径 |
| `CHROME_PATH` | 强制 Chromium |
| `CLOAKBROWSER_SUPPRESS_FONT_WARNING` | 抑制 Linux 字体提示 |
| `EDITOR` | `grok config` 编辑器 |

完整模板：`~/.grok/config.env.example`（每次 start/config 同步）。

---

## 流水线

```text
清障预热 → S:Turnstile → P:邮箱+验证码 → C:注册拿 SSO
       → OAuth → 整备 CPA JSON → 探活 → 写 CPA/
       → (可选) 异步上传 Management API
```

- **TARGET**：仅 `CPA/` 探活成功数  
- **座位**：`done + reserved ≤ target`（全局，非每线程）  
- 自动上传失败**不**影响记成功  

---

## 边缘 / 清障

```env
CF_IMPERSONATE=chrome_131
CF_IMPERSONATE_FALLBACK=chrome_124,chrome_120
CLEARANCE_MODE=auto    # auto | always | never
CLEARANCE_ENABLED=1
CLEARANCE_AUTO_STOP=1
```

| CLEARANCE_MODE | 行为 |
|----------------|------|
| **auto**（默认） | 先 TLS 指纹 warm；403/拦再 `docker compose up` + 预热 |
| **always** | 启动即清障 |
| **never** | 不碰 Docker 清障（靠直连/自有代理） |

## Turnstile

默认 `browser` + **`TURNSTILE_MODE=offscreen`**：

1. 常驻池 `turnstile_pool.py`（屏外有头）  
2. 回退 one-shot `turnstile_mint.py`  
3. 再回退 chromedp 真 headless（不推荐，易 600010）  

```env
TURNSTILE_PROVIDER=browser
TURNSTILE_MODE=offscreen   # offscreen | headless | auto
```

默认**不**注入 FlareSolverr cookie/UA（除非 `GROK_TURNSTILE_INJECT_CLEARANCE=1`）。

可选 lite farm：

```env
TURNSTILE_PROVIDER=lite
LITE_SOLVER_URL=http://127.0.0.1:5072
```

### 代理：WARP vs HTTP 池

| | WARP + Privoxy（默认） | HTTP 代理池 |
|--|------------------------|-------------|
| 成本 | 低 | 按量 |
| 适合 | 个人小批量 | 冲量 / 多 IP |
| 配置 | 本机 compose | 池 + 轮换 |

---

## CPA 上传

```env
CPA_UPLOAD_ENABLED=1
CPA_MANAGEMENT_BASE=http://127.0.0.1:8317/v0/management
CPA_MANAGEMENT_KEY=...
```

- 宿主机跑 `grok` 必须用 `127.0.0.1`，不要写 `cli-proxy-api`  
- 新版本会自动改写 docker 主机名并补 `/v0/management`  
- 手动：`grok upload`

---

## 目录结构

```text
Grok-Register/
├── cmd/grok/
├── internal/
├── scripts/
│   ├── install.sh            # 一键部署
│   ├── turnstile_mint.py
│   ├── turnstile_pool.py
│   └── requirements-turnstile.txt
├── clearance/                # docker compose 清障
├── cloudflare/email-worker.js
├── Makefile                  # APP= 可改命令名
└── README.md
```

---

## 常见问题

**一键装完命令找不到**

```bash
export PATH=$PATH:/usr/local/bin
# 或重新登录 shell；见 /etc/profile.d/grok-register.sh
```

**`make build` go not found**

```bash
export PATH=$PATH:/usr/local/go/bin
make build && sudo make install
```

**`turnstile timeout` / `iframes=0`**

1. `GROK_PYTHON` 指向已装 playwright 的 venv  
2. `python -m cloakbrowser install` 已完成  
3. clearance healthy，`REGISTER_PROXY` 可用  

**`lookup cli-proxy-api: no such host`**

```env
CPA_MANAGEMENT_BASE=http://127.0.0.1:8317/v0/management
```

**邮箱建得特别多**

请更新到含全局 `reserved` 座位上限的版本。

---

## 更新日志（近期）

### 2026-07 · OAuth / 运维 / 导出

- **OAuth**：全局启动间隔（默认 4s）、OIDC discovery 缓存、`rate_limited` 自动重试一次；高并发默认 1 个 OAuth worker
- **探活**：`PROBE_WARMUP_SEC` 默认 1.5s；403 重试间隔缩短
- **`grok stop`**：在 `CLEARANCE_AUTO_STOP=1` 时同步 `docker compose stop` 清障栈（以前只有达目标/正常结束才停）
- **安装安全**：非默认 `INSTALL_DIR` 且无 `.git` 时不再 `rm -rf` 整目录；危险/非空非本仓目录直接拒绝
- **grok2api**：`outputs/<run>/grok2api/tokens.txt` 仅 SSO token，一行一个
- **一键安装**：Linux 默认装 `xvfb`；种子/升级合并 `TURNSTILE_MODE=offscreen`、`CF_IMPERSONATE`、`OAUTH_*`、`PROBE_WARMUP_SEC` 等

### 更早（协议优先批次）

- TLS `chrome_131` + `CLEARANCE_MODE=auto|always|never`
- Turnstile `offscreen`（有 `$DISPLAY`/`xvfb-run` 时有头屏外；无显示则 headless 回退）
- CPA Management 上传结束前等待；座位 `done+reserved≤target`
- 分区中文 `config.env` 模板；交互 WARP/代理/自动停容器

### 旧版本升级（命令集合）

**Linux（Debian/Ubuntu）**

```bash
# 1) 系统包（无头 Turnstile 需要 xvfb）
sudo apt-get update -y
sudo apt-get install -y xvfb

# 2) 更新源码 + 重装 CLI（推荐重跑一键，会保留 config 并补齐新键）
curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | sudo bash

# 或手动:
#   cd /opt/Grok-Register && sudo git pull
#   export PATH=$PATH:/usr/local/go/bin
#   make build && sudo make install

# 3) 若未跑一键 merge，手工补 config（路径按你的 GROK_HOME）
CFG="${GROK_HOME:-$HOME/.grok}/config.env"
# root 安装时常是 /home/<用户>/.grok
grep -q '^CLEARANCE_MODE=' "$CFG" 2>/dev/null || echo 'CLEARANCE_MODE=auto' >>"$CFG"
grep -q '^CLEARANCE_AUTO_STOP=' "$CFG" 2>/dev/null || echo 'CLEARANCE_AUTO_STOP=1' >>"$CFG"
grep -q '^CF_IMPERSONATE=' "$CFG" 2>/dev/null || echo 'CF_IMPERSONATE=chrome_131' >>"$CFG"
grep -q '^CF_IMPERSONATE_FALLBACK=' "$CFG" 2>/dev/null || echo 'CF_IMPERSONATE_FALLBACK=chrome_124,chrome_120' >>"$CFG"
grep -q '^TURNSTILE_MODE=' "$CFG" 2>/dev/null || echo 'TURNSTILE_MODE=offscreen' >>"$CFG"
grep -q '^OAUTH_MIN_INTERVAL_SEC=' "$CFG" 2>/dev/null || echo 'OAUTH_MIN_INTERVAL_SEC=4' >>"$CFG"
grep -q '^OAUTH_RETRY_SEC=' "$CFG" 2>/dev/null || echo 'OAUTH_RETRY_SEC=45' >>"$CFG"
grep -q '^PROBE_WARMUP_SEC=' "$CFG" 2>/dev/null || echo 'PROBE_WARMUP_SEC=1.5' >>"$CFG"

# 4) 验证
which grok; grok help
command -v xvfb-run && xvfb-run -a echo xvfb_ok
```

**macOS**

```bash
# 1) 依赖（一般无需 xvfb；Docker Desktop 保持运行）
brew install go python 2>/dev/null || true

# 2) 一键或手动
curl -fsSL https://raw.githubusercontent.com/Charles-0509/Grok-Register/main/scripts/install.sh | bash
# 或: cd ~/Grok-Register && git pull && make build && make install PREFIX=$HOME/.local

# 3) 补 config（同 Linux，CFG=~/.grok/config.env）
CFG="${GROK_HOME:-$HOME/.grok}/config.env"
grep -q '^OAUTH_MIN_INTERVAL_SEC=' "$CFG" 2>/dev/null || echo 'OAUTH_MIN_INTERVAL_SEC=4' >>"$CFG"
grep -q '^OAUTH_RETRY_SEC=' "$CFG" 2>/dev/null || echo 'OAUTH_RETRY_SEC=45' >>"$CFG"
grep -q '^PROBE_WARMUP_SEC=' "$CFG" 2>/dev/null || echo 'PROBE_WARMUP_SEC=1.5' >>"$CFG"
grep -q '^TURNSTILE_MODE=' "$CFG" 2>/dev/null || echo 'TURNSTILE_MODE=offscreen' >>"$CFG"
grep -q '^CLEARANCE_AUTO_STOP=' "$CFG" 2>/dev/null || echo 'CLEARANCE_AUTO_STOP=1' >>"$CFG"
```

可选调参：OAuth 仍限流可把 `OAUTH_MIN_INTERVAL_SEC` 调到 `6`–`8`，或 `OAUTH_WORKERS=1`。

---

## 开发

```bash
go test ./...
go build -o bin/grok ./cmd/grok
bash -n scripts/install.sh
```

---

## License

MIT（与上游 grok-free-register 思路一致；本仓库为 Go 重制版。）

---

## 友链

- [LinuxDo · Charles0509](https://linux.do/u/charles0509)
