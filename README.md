# Grok-Register

Grok 免费号 **注册 → OAuth → CPA 可用 JSON** 二合一 CLI（Go）。

一条命令后台跑完，产物可直接导入 CPA / cliproxy 类网关。

```bash
grok start -t 10
grok status
grok logs -f
grok stop
grok upload    # 手动上传 CPA JSON 到 Management API
```

---

## 功能

- 临时邮箱 / 自建域名邮箱注册
- 注册成功后立刻 Device Flow OAuth
- 整备 `cli-chat-proxy` + grok-cli headers 的 CPA JSON
- 可选探活；可选自动上传到 CPA Management API
- 内置 Cloudflare 清障 compose（WARP + Privoxy + FlareSolverr）
- Turnstile：默认 **Playwright + CloakBrowser**（与原 Python 注册机同路径），可选 lite farm

---

## 系统要求

| 组件 | 用途 | 不装会怎样 |
|------|------|------------|
| Go 1.21+ | 仅编译 `grok` | 无法 build |
| Python 3.10+ + venv | Turnstile Playwright mint | 拿不到 token |
| Playwright + CloakBrowser | 无头过 CF Turnstile | `timeout` / `iframes=0` |
| CloakBrowser Chromium | 指纹相对稳的无头 Chrome | mint 失败率高 |
| Docker | 清障栈（强烈推荐） | 注册/邮箱/CF 更容易挂 |
| CPA Management（可选） | `grok upload` / 自动上传 | 本地仍有 `CPA/*.json` |

---

## 完整部署（Debian / Ubuntu）

> 目标：系统依赖 → Go → Docker → 编译 `grok` → **无头浏览器** → 清障栈 → 配置 → 跑注册。  
> 以下以 root 或 sudo 为例；路径可按需改。

### 0. 系统依赖

```bash
sudo apt update
sudo apt install -y \
  git curl ca-certificates gnupg lsb-release \
  build-essential \
  python3 python3-pip python3-venv \
  # Chromium / Playwright 常见系统库（无头环境很重要）
  libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libcups2 \
  libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 \
  libxrandr2 libgbm1 libasound2t64 libpango-1.0-0 libcairo2 \
  fonts-liberation fonts-noto-cjk
```

> 若 `libasound2t64` 不存在，改成 `libasound2`。

### 1. 安装 Go（仅编译需要，建议 1.21+）

```bash
cd /tmp
# 版本号请按 https://go.dev/dl/ 更新
curl -fsSL -o go.tgz https://go.dev/dl/go1.24.4.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go.tgz
echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh
export PATH=$PATH:/usr/local/go/bin
go version
```

### 2. 安装 Docker（清障栈用）

```bash
# 已有 docker 可跳过
curl -fsSL https://get.docker.com | sudo sh
sudo systemctl enable --now docker
docker compose version || sudo apt install -y docker-compose-plugin
```

### 3. 拉取并编译安装 Grok-Register

```bash
sudo mkdir -p /opt
cd /opt
sudo git clone https://github.com/Charles-0509/Grok-Register.git
cd /opt/Grok-Register

export PATH=$PATH:/usr/local/go/bin
make build
sudo make install
# 安装结果：
#   /usr/local/bin/grok
#   /usr/local/share/grok-reg/turnstile_mint.py

grok help
```

`sudo make install` 在已有 `bin/grok` 时**不会**再调 `go`（避免 root PATH 里没有 go）。

### 4. 无头浏览器：Playwright + CloakBrowser（**必做**）

Turnstile 默认本机 mint，**只装 `grok` 二进制不够**。

```bash
# 独立 venv（推荐固定路径，方便 root 跑）
sudo python3 -m venv /opt/cloakbrowser-venv
sudo /opt/cloakbrowser-venv/bin/pip install -U pip
sudo /opt/cloakbrowser-venv/bin/pip install -r /opt/Grok-Register/scripts/requirements-turnstile.txt

# 下载 CloakBrowser 自带 Chromium → ~/.cloakbrowser
# root 跑则装到 /root/.cloakbrowser
sudo /opt/cloakbrowser-venv/bin/python -m cloakbrowser install

# （可选）系统缺库时再执行
# sudo /opt/cloakbrowser-venv/bin/playwright install-deps chromium

# 写进环境（root 长期跑）
echo 'export GROK_PYTHON=/opt/cloakbrowser-venv/bin/python' | sudo tee -a /root/.bashrc
echo 'export CLOAKBROWSER_SUPPRESS_FONT_WARNING=1' | sudo tee -a /root/.bashrc
export GROK_PYTHON=/opt/cloakbrowser-venv/bin/python
export CLOAKBROWSER_SUPPRESS_FONT_WARNING=1
```

可选环境变量：

```bash
# 一般 make install 后不用改脚本路径
# export GROK_TURNSTILE_SCRIPT=/usr/local/share/grok-reg/turnstile_mint.py
# 或：/opt/Grok-Register/scripts/turnstile_mint.py

# 强制指定 Chrome（通常自动探测 ~/.cloakbrowser）
# export CHROME_PATH=/root/.cloakbrowser/chromium-xxx/chrome
```

**冒烟测试**（清障栈起来后，应打印长 token 且 exit 0）：

```bash
export GROK_PYTHON=/opt/cloakbrowser-venv/bin/python
$GROK_PYTHON /usr/local/share/grok-reg/turnstile_mint.py \
  --site-key 0x4AAAAAAAhr9JGVDZbrZOo0 \
  --url https://accounts.x.ai/sign-up \
  --proxy http://127.0.0.1:40080 \
  --timeout 70
echo exit:$?
```

### 5. 清障栈（WARP + Privoxy + FlareSolverr，强烈推荐）

```bash
cd /opt/Grok-Register/clearance
sudo docker compose up -d
sudo docker compose ps
# 期望：grok-clearance-warp / privoxy / flaresolverr 均为 healthy
```

端口（仅本机回环）：

| 端口 | 服务 |
|------|------|
| `127.0.0.1:40000` | WARP SOCKS5 |
| `127.0.0.1:40080` | Privoxy HTTP（注册 / 浏览器代理） |
| `127.0.0.1:8191` | FlareSolverr |

检查：

```bash
curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8191/
curl -x http://127.0.0.1:40080 -sS -o /dev/null -w '%{http_code}\n' \
  https://www.cloudflare.com/cdn-cgi/trace
```

> 本机若已有其它占用 `40000/40080/8191` 的 compose，先停掉再起。

### 6. 配置 `~/.grok/config.env`

首次 `grok start` 会交互生成；也可手动：

```bash
sudo mkdir -p /root/.grok
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
PHYSICAL_CAP=0

# CPA 上传：宿主机 grok 必须用 127.0.0.1，不要写 docker 服务名 cli-proxy-api
# 路径需含 /v0/management（上传会再拼 /auth-files）
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

# 2) testmail.app（GitHub Student Pack Essential 等）
# EMAIL_MODE=testmail
# TESTMAIL_API_KEY=你的_apikey
# TESTMAIL_NAMESPACE=你的_namespace
# TESTMAIL_DOMAIN=inbox.testmail.app
# 地址形态：{namespace}.{tag}@inbox.testmail.app — tag 自动随机，无需预注册

# 3) 自建域名
# EMAIL_MODE=custom
# EMAIL_DOMAIN=example.com
# EMAIL_API=http://127.0.0.1:8080
```

参考 `cloudflare/email-worker.js` 配置 Cloudflare Email Routing catch-all。

`tempmail` 默认公共 **tempmail.lol** + mail.tm 系 fallback，**无需私人 API Token**。  
`testmail` 需要 console 里的 **API key + namespace**（密钥只写本地 `config.env`，勿提交仓库）。
### 7. 启动与运维

```bash
export PATH=$PATH:/usr/local/go/bin
export GROK_PYTHON=/opt/cloakbrowser-venv/bin/python
export CLOAKBROWSER_SUPPRESS_FONT_WARNING=1

# 后台跑；目标 N = 探活成功写入 CPA/ 的数量
grok start -t 10
grok status
grok logs -f
grok stop

# 手动上传最近 run 的 CPA JSON 到 Management API
grok upload
```

**数据目录**（`GROK_HOME` 可覆盖，默认 `~/.grok`，root 为 `/root/.grok`）：

```text
~/.grok/
├── config.env
├── run.pid / run.lock / state.json
├── logs/run-yyyymmdd-HHMMSS.log
└── outputs/
    └── yyyymmdd-HHMMSS/
        ├── SSO/          # accounts.txt, auth-sessions.jsonl
        ├── CPA/          # 探活成功的 CPA JSON（可导入）
        └── discarded/    # 探活失败
```

### 8. 更新版本

```bash
cd /opt/Grok-Register
sudo git pull
export PATH=$PATH:/usr/local/go/bin
make build && sudo make install
# 若 scripts/requirements 有变：
sudo /opt/cloakbrowser-venv/bin/pip install -r scripts/requirements-turnstile.txt
```

### macOS 备注

- Go / Docker Desktop 自行安装即可  
- Turnstile：同样 `python3 -m venv` + `pip install -r scripts/requirements-turnstile.txt` + `python -m cloakbrowser install`  
- 清障栈：`cd clearance && docker compose up -d`  
- Chrome 也可使用系统 Google Chrome（`CHROME_PATH` 可选）

---

## 命令一览

| 命令 | 说明 |
|------|------|
| `grok start` | 后台启动，默认目标 10 |
| `grok start -t N` | 目标 N（1–10000）；**计数 = 探活成功写入 CPA 的数量** |
| `grok status` | 未运行 / 运行中 / 错误；进度、线程、当前步骤 |
| `grok logs` | 最近一次完整日志 |
| `grok logs -f` | 实时跟踪日志 |
| `grok stop` | 立即停止 |
| `grok upload` | 交互选择最近 10 次 run，上传其中 CPA JSON |

---

## 配置补充（`~/.grok/config.env`）

完整模板见 `config.env.example`。

### 环境变量（进程级）

| 变量 | 说明 |
|------|------|
| `GROK_HOME` | 数据根目录，默认 `~/.grok` |
| `GROK_PYTHON` | 跑 `turnstile_mint.py` 的 Python |
| `GROK_TURNSTILE_SCRIPT` | mint 脚本路径 |
| `CHROME_PATH` | 强制指定 Chromium 可执行文件 |
| `CLOAKBROWSER_SUPPRESS_FONT_WARNING` | 抑制 Linux 字体提示（可选） |

---

## 流水线

```text
清障预热 → S:Turnstile → P:邮箱+验证码 → C:注册拿 SSO
       → 立刻 OAuth (HTTP device verify/approve)
       → 整备 CPA JSON → 探活 → 写 CPA/
       → (可选) 异步上传 Management API
```

- **TARGET**：仅 `CPA/` 探活成功计数  
- **自动上传失败**不影响账号记为成功  
- **邮箱预创建**按 target 限流，避免 target=5 时狂开邮箱  

---

## Turnstile 说明

默认 `browser`：

1. **常驻池** `scripts/turnstile_pool.py`：启动 N 个 CloakBrowser 进程，**复用浏览器、并行 mint**（`TURNSTILE_WORKERS`，默认约 2）  
2. 池不可用时回退 one-shot `scripts/turnstile_mint.py`  
3. 再回退 chromedp（CF 下通常更差）  

```env
TURNSTILE_PROVIDER=browser
TURNSTILE_WORKERS=2   # 并行槽位；机器内存紧可改 1，富裕可试 3–4
```

可选外接 YesCaptcha 形 farm：

```env
TURNSTILE_PROVIDER=lite
LITE_SOLVER_URL=http://127.0.0.1:5072
```

仓库**不内置** farm 镜像。

### 代理：WARP 清障 vs HTTP 代理池

| | **WARP + Privoxy（本仓库默认）** | **HTTP 代理池** |
|--|----------------------------------|-----------------|
| 成本 | 低 / 免费 | 按量或包月 |
| 出口质量 | 中等；CF 友好但 IP 相对固定、易被限 | 可换大量 IP；质量参差 |
| 配置 | 本机 compose 即可 | 需池服务 + 轮换逻辑 |
| 适合 | 个人小批量、本机注册机 | 大批量、强隔离、规避单 IP 限流 |
| 风险 | 同出口并发高时限流 | 垃圾代理导致 Turnstile/注册失败 |

**建议：** 单机自用优先 **WARP 清障**；要冲量再上可靠 HTTP 池，并把 `REGISTER_PROXY` / 浏览器 mint 代理指到池。两者可并存（注册走池、其它走直连），但当前默认设计是 **统一走 `REGISTER_PROXY`**。
---

## CPA 上传

### 自动

`CPA_UPLOAD_ENABLED=1` 且配置了 `CPA_MANAGEMENT_KEY` 时，每个成功 CPA JSON 会异步：

- 优先 `multipart` 字段 `file` → `POST .../auth-files`  
- 失败时回退 raw JSON + `?name=`  
- Header：`Authorization: Bearer` + `X-Management-Key`  
- 日志**不打印**密钥  

### 手动

```bash
grok upload
# 列出最近 10 个 outputs/<run_id>/
# 输入 1 或 1,2,3 多选上传
```

### 宿主机 vs Docker 网络

`grok` 跑在**宿主机**时：

```env
# ✅ 正确
CPA_MANAGEMENT_BASE=http://127.0.0.1:8317/v0/management

# ❌ 错误：cli-proxy-api 仅 compose 内可解析
# CPA_MANAGEMENT_BASE=http://cli-proxy-api:8317
```

新版本会自动把 `cli-proxy-api` 等服务名改写为 `127.0.0.1`，并补上 `/v0/management`（若缺失）。

---

## 目录结构

```text
Grok-Register/
├── cmd/grok/                 # CLI 入口
├── internal/                 # 业务包
│   ├── clearance/            # FlareSolverr prewarm
│   ├── turnstile/            # Playwright bridge + chromedp fallback + lite
│   ├── pipeline/             # S/P/C + OAuth + CPA
│   └── cpa/                  # 落盘 + Management 上传
├── scripts/
│   ├── turnstile_mint.py     # one-shot mint
│   ├── turnstile_pool.py     # 常驻多浏览器并行 mint
│   └── requirements-turnstile.txt
├── clearance/                # docker compose 清障栈
├── cloudflare/email-worker.js
├── config.env.example
├── Makefile
└── README.md
```

---

## 常见问题

**`make build` / `sudo make install` 报 go not found**

```bash
export PATH=$PATH:/usr/local/go/bin
make build
sudo make install          # 已有 bin/grok 时不再调用 go
# 或：sudo install -m 755 bin/grok /usr/local/bin/grok
```

**`turnstile timeout` / `iframes=0`**

1. 确认 `GROK_PYTHON` 指向已装 playwright 的 venv  
2. `python -m cloakbrowser install` 已完成  
3. `clearance` 容器 healthy，`REGISTER_PROXY` 可用  
4. `grok logs -f` 中是否出现 `playwright mint: ...` 具体错误  

**`lookup cli-proxy-api: no such host`**

宿主机跑 `grok`，`CPA_MANAGEMENT_BASE` 用 `http://127.0.0.1:8317/v0/management`。

**邮箱建得特别多**

新版本会按 target 限制 P/Q；请更新到最新代码并 `make build && make install`。

**只想手动导入 CPA**

看 `~/.grok/outputs/<run>/CPA/*.json`，或 `grok upload`。

---

## 开发

```bash
go test ./...
go build -o bin/grok ./cmd/grok
```

---

## License

MIT（与上游 grok-free-register 思路一致；本仓库为 Go 重制版。）

---

## 友链

- [LinuxDo · Charles0509](https://linux.do/u/charles0509)
