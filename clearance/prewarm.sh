#!/usr/bin/env bash
# Hit FlareSolverr for each x.ai root host and print cf_clearance status.
# Does not start containers — run `docker compose up -d` in this directory first.
#
# Usage:
#   bash clearance/prewarm.sh
#   FLARESOLVERR_URL=http://127.0.0.1:8191 CLEARANCE_PROXY=http://privoxy:8118 bash clearance/prewarm.sh
#   bash clearance/prewarm.sh --direct   # no proxy inside FS browser

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export FLARESOLVERR_URL="${FLARESOLVERR_URL:-http://127.0.0.1:8191}"
export CLEARANCE_TIMEOUT_SEC="${CLEARANCE_TIMEOUT_SEC:-90}"

if [[ "${1:-}" == "--direct" ]]; then
  export CLEARANCE_PROXY=""
elif [[ -n "${CLEARANCE_PROXY+x}" ]]; then
  : # keep caller value (including empty)
else
  # Default: FS browser exits via compose Privoxy→WARP (NOT host network).
  # Bare prewarm without proxy often gets Connection reset (FS has no WARP).
  export CLEARANCE_PROXY="${CLEARANCE_PROXY:-http://privoxy:8118}"
fi

# Wait until FlareSolverr answers (compose health: starting is too early).
echo "[prewarm] waiting for flaresolverr at ${FLARESOLVERR_URL} …"
for i in $(seq 1 40); do
  if curl -fsS -m 2 "${FLARESOLVERR_URL}/" >/dev/null 2>&1; then
    echo "[prewarm] flaresolverr ready (${i}s)"
    break
  fi
  if [[ "$i" -eq 40 ]]; then
    echo "[prewarm] ERROR: flaresolverr not ready after 40s — docker compose ps / logs" >&2
    exit 1
  fi
  sleep 1
done

# Prefer project helper when venv/python path exists; always fall back to stdlib.
exec python3 - <<'PY'
from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request
from urllib.parse import urlparse

DEFAULT_URLS = [
    "https://accounts.x.ai",
    "https://x.ai",
    "https://status.x.ai",
    "https://console.x.ai",
    "https://auth.x.ai",
]

def urls() -> list[str]:
    raw = (os.environ.get("CLEARANCE_URLS") or "").strip()
    if not raw:
        return list(DEFAULT_URLS)
    out: list[str] = []
    seen: set[str] = set()
    for part in raw.split(","):
        item = part.strip()
        if not item:
            continue
        if "://" not in item:
            item = "https://" + item
        p = urlparse(item)
        if p.scheme and p.netloc:
            item = f"{p.scheme}://{p.netloc}"
        if item not in seen:
            seen.add(item)
            out.append(item)
    return out or list(DEFAULT_URLS)

fs = (os.environ.get("FLARESOLVERR_URL") or "http://127.0.0.1:8191").rstrip("/")
proxy = (os.environ.get("CLEARANCE_PROXY") or "").strip()
timeout = max(10, int(os.environ.get("CLEARANCE_TIMEOUT_SEC") or 60))
max_ms = timeout * 1000

print(f"[prewarm] flaresolverr={fs}")
print(f"[prewarm] clearance_proxy={proxy or '(direct FS egress)'}")
print()

ok = fail = 0
for url in urls():
    host = (urlparse(url).hostname or url).lower()
    payload: dict = {
        "cmd": "request.get",
        "url": url,
        "maxTimeout": max_ms,
    }
    if proxy:
        payload["proxy"] = {"url": proxy}
    body = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        f"{fs}/v1",
        data=body,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout + 15) as resp:
            data = json.loads(resp.read().decode("utf-8"))
        elapsed = round(time.time() - t0, 2)
        status = str(data.get("status") or "").lower()
        if status != "ok":
            print(f"  FAIL  {host}  {elapsed}s  status={data.get('status')} msg={data.get('message')}")
            fail += 1
            continue
        sol = data.get("solution") if isinstance(data.get("solution"), dict) else {}
        cookies = sol.get("cookies") or []
        n = len(cookies) if isinstance(cookies, list) else 0
        cf = any(isinstance(c, dict) and c.get("name") == "cf_clearance" for c in (cookies or []))
        print(f"  OK    {host}  {elapsed}s  http={sol.get('status')}  cookies={n}  cf_clearance={'yes' if cf else 'no'}")
        ok += 1
    except Exception as exc:  # noqa: BLE001
        elapsed = round(time.time() - t0, 2)
        print(f"  FAIL  {host}  {elapsed}s  {exc}")
        fail += 1

print()
print(f"[prewarm] done ok={ok} fail={fail}")
sys.exit(0 if fail == 0 else 1)
PY
