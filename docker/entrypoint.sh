#!/usr/bin/env bash
# Docker entrypoint for Grok-Register.
#
# Two modes (pass as $1 or via env MODE=...):
#
#   idle   (default)  `docker compose up -d grok` 长跑。
#                     进容器用 `docker exec -it grok-reg grok start -t 10` 等交互命令。
#                     PID 1 = tail -f /dev/null，所有 grok 后台 worker 会被 reaped。
#
#   run               `docker compose run --rm grok run` 一次性前台跑一次 batch。
#                     直接 exec grok --worker --target N，绕过 daemon fork（容器主进程即 worker）。
#
# 关键：容器主进程就是前台进程，所有 daemon.go 里 syscall.Flock / Setsid / signal 等
# Unix-only 调用在 Linux 容器内正常工作 — Windows host 完全不参与。

set -euo pipefail

mode="${1:-${MODE:-idle}}"
# `MODE=run` 也可能作为环境变量传入，shell 形式 CMD 不带参数；兼容两者。
if [ "$mode" = "" ]; then mode=idle; fi

# GROK_HOME 必须存在且可写。compose 挂的 ./data 这里自动建。
mkdir -p "${GROK_HOME:-/data}/logs" "${GROK_HOME:-/data}/outputs"

# 若用户没自带 config.env，挂一份默认进去。
if [ ! -f "${GROK_HOME:-/data}/config.env" ] && [ -f "/etc/grok/config.env" ]; then
    cp /etc/grok/config.env "${GROK_HOME:-/data}/config.env"
fi

case "$mode" in
  idle)
    echo "[grok-docker] idle 模式 - 用 docker exec -it grok-reg grok ..."
    echo "[grok-docker]   例: docker exec -it grok-reg grok start -t 10"
    echo "[grok-docker]         docker exec -it grok-reg grok status"
    echo "[grok-docker]         docker exec -it grok-reg grok logs -f"
    exec tail -f /dev/null
    ;;

  run|worker)
    target="${GROK_TARGET:-${TARGET:-10}}"
    echo "[grok-docker] runner 模式 - 前台跑 worker，target=${target}"
    # 生成一个 runID（与 grok 内部 NewRunID 同格式），便于 compose logs -t 区分
    run_id="${GROK_RUN_ID:-$(date +%Y%m%d-%H%M%S)}"
    exec grok --worker --target "${target}" --run-id "${run_id}"
    ;;

  shell|sh|bash)
    exec /bin/bash
    ;;

  *)
    # 直接把任意子命令转发给 grok：`docker run ... grok status`
    exec grok "$@"
    ;;
esac