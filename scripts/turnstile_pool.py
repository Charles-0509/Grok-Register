#!/usr/bin/env python3
"""Persistent CloakBrowser pool for parallel Turnstile mint.

Go launches this once; workers speak newline-delimited JSON:

  stdin:  {"id":1,"site_key":"...","url":"...","timeout":90,"proxy":"...","chrome":"..."}
  stdout: {"id":1,"ok":true,"token":"..."}  or  {"id":1,"ok":false,"error":"..."}

Each pool slot owns one Chromium process; mint uses a fresh context/page.
"""
from __future__ import annotations

import argparse
import asyncio
import glob
import json
import os
import sys
import time
from typing import Any


def find_chrome() -> str:
    env = (os.environ.get("CHROME_PATH") or "").strip()
    if env and os.path.exists(env):
        return env
    homes = []
    h = os.path.expanduser("~")
    if h:
        homes.append(h)
    homes.extend(["/root", "/home/charles"])
    matches: list[str] = []
    for home in homes:
        base = os.path.join(home, ".cloakbrowser")
        matches.extend(glob.glob(os.path.join(base, "chromium-*/chrome")))
        matches.extend(
            glob.glob(
                os.path.join(
                    base,
                    "chromium-*/Chromium.app/Contents/MacOS/Chromium",
                )
            )
        )
    if matches:
        return sorted(matches)[-1]
    for p in (
        "/usr/bin/google-chrome",
        "/usr/bin/google-chrome-stable",
        "/usr/bin/chromium",
        "/usr/bin/chromium-browser",
    ):
        if os.path.exists(p):
            return p
    return ""


def emit(obj: dict[str, Any]) -> None:
    sys.stdout.write(json.dumps(obj, ensure_ascii=False) + "\n")
    sys.stdout.flush()


def launch_args(mode: str) -> list[str]:
    args = [
        "--no-sandbox",
        "--disable-blink-features=AutomationControlled",
        "--no-first-run",
        "--no-default-browser-check",
        "--disable-infobars",
        "--disable-dev-shm-usage",
    ]
    if mode in ("", "auto", "offscreen"):
        args.extend(["--window-position=-32000,-32000", "--window-size=800,600"])
    return args


class Slot:
    def __init__(self, chrome: str, proxy: str, mode: str = "offscreen") -> None:
        self.chrome = chrome
        self.proxy = proxy
        self.mode = (mode or "offscreen").strip().lower() or "offscreen"
        if self.mode == "auto":
            self.mode = "offscreen"
        self.browser = None
        self.pw = None
        self.lock = asyncio.Lock()
        self.fail_streak = 0

    async def ensure(self) -> None:
        if self.browser is not None:
            return
        from playwright.async_api import async_playwright

        use_headless = self.mode == "headless"
        launch: dict = {
            "executable_path": self.chrome,
            "headless": use_headless,
            "args": launch_args(self.mode),
        }
        if self.proxy:
            launch["proxy"] = {"server": self.proxy}
        self.pw = await async_playwright().start()
        self.browser = await self.pw.chromium.launch(**launch)

    async def close(self) -> None:
        try:
            if self.browser is not None:
                await self.browser.close()
        except Exception:
            pass
        self.browser = None
        try:
            if self.pw is not None:
                await self.pw.stop()
        except Exception:
            pass
        self.pw = None

    async def reset(self) -> None:
        await self.close()
        await self.ensure()


async def mint_on_slot(
    slot: Slot,
    site_key: str,
    page_url: str,
    timeout: float,
    ua: str,
) -> str:
    await slot.ensure()
    assert slot.browser is not None
    ctx_kwargs: dict = {"viewport": {"width": 800, "height": 600}}
    if ua:
        ctx_kwargs["user_agent"] = ua
    context = await slot.browser.new_context(**ctx_kwargs)
    try:
        await context.add_init_script(
            'Object.defineProperty(navigator,"webdriver",{get:()=>undefined})'
        )
        page = await context.new_page()
        await page.goto(page_url, timeout=45000, wait_until="domcontentloaded")
        await page.wait_for_timeout(1200)

        inject = (
            "var d=document.createElement('div');"
            "d.className='cf-turnstile';"
            f"d.setAttribute('data-sitekey','{site_key}');"
            "d.style.cssText='position:fixed;top:10px;left:10px;z-index:99999;"
            "background:white;padding:12px;border:2px solid red;border-radius:6px;"
            "width:300px;height:70px';"
            "document.body.appendChild(d);"
            "function __r(){"
            "window.turnstile&&window.turnstile.render(d,{"
            f"sitekey:'{site_key}',"
            "callback:function(t){"
            'var i=document.querySelector(\'input[name="cf-turnstile-response"]\');'
            "if(!i){i=document.createElement('input');i.type='hidden';"
            "i.name='cf-turnstile-response';document.body.appendChild(i);}"
            "i.value=t;"
            "}})}"
            "if(window.turnstile){__r()}"
            "else{var s=document.createElement('script');"
            "s.src='https://challenges.cloudflare.com/turnstile/v0/api.js';"
            "s.onload=function(){setTimeout(__r,1000)};"
            "document.head.appendChild(s);}"
        )
        await page.evaluate(inject)
        await page.wait_for_timeout(500)

        async def read_token() -> str:
            try:
                return await page.evaluate(
                    'document.querySelector(\'input[name="cf-turnstile-response"]\')?.value||""'
                )
            except Exception:
                return ""

        async def click_center() -> None:
            box = await page.evaluate(
                """() => {
                const e = document.querySelector('.cf-turnstile');
                if (!e) return null;
                const r = e.getBoundingClientRect();
                return {x: r.left + r.width / 2, y: r.top + r.height / 2};
            }"""
            )
            if not box:
                return
            x, y = float(box["x"]), float(box["y"])
            await page.mouse.move(max(0, x - 25), max(0, y - 8))
            await page.mouse.move(x, y, steps=8)
            await page.mouse.down()
            await asyncio.sleep(0.05)
            await page.mouse.up()

        for _ in range(2):
            t = await read_token()
            if t and len(t) > 10:
                return t
            await page.wait_for_timeout(800)

        for i in range(3):
            t = await read_token()
            if t and len(t) > 10:
                return t
            try:
                await click_center()
            except Exception:
                pass
            await page.wait_for_timeout(600)

        deadline = time.time() + timeout
        i = 0
        while time.time() < deadline:
            await page.wait_for_timeout(500)
            t = await read_token()
            if t and len(t) > 10:
                return t
            if i > 0 and i % 20 == 0:
                try:
                    await click_center()
                except Exception:
                    pass
            i += 1

        try:
            diag = await page.evaluate(
                """() => {
                const ifr=[...document.querySelectorAll('iframe')].filter(f=>{
                  const s=f.src||'';
                  return s.includes('turnstile')||s.includes('challenges.cloudflare.com');
                }).length;
                return {
                  iframes: ifr,
                  all_ifr: document.querySelectorAll('iframe').length,
                  widget: !!document.querySelector('.cf-turnstile'),
                  turnstile: !!window.turnstile,
                  title: document.title||'',
                };
            }"""
            )
            raise RuntimeError(f"turnstile timeout (no token) diag={diag}")
        except RuntimeError:
            raise
        except Exception:
            raise RuntimeError("turnstile timeout (no token)")
    finally:
        try:
            await context.close()
        except Exception:
            pass


class Pool:
    def __init__(self, size: int, chrome: str, proxy: str, mode: str = "offscreen") -> None:
        self.slots = [Slot(chrome, proxy, mode=mode) for _ in range(max(1, size))]
        self.queue: asyncio.Queue[Slot] = asyncio.Queue()
        for s in self.slots:
            self.queue.put_nowait(s)

    async def warm(self) -> None:
        await asyncio.gather(*(s.ensure() for s in self.slots))

    async def mint(self, req: dict[str, Any]) -> dict[str, Any]:
        rid = req.get("id")
        site_key = str(req.get("site_key") or "")
        page_url = str(req.get("url") or "https://accounts.x.ai/sign-up")
        timeout = float(req.get("timeout") or 90)
        ua = str(req.get("ua") or "")
        if not site_key:
            return {"id": rid, "ok": False, "error": "missing site_key"}

        slot: Slot = await self.queue.get()
        try:
            async with slot.lock:
                try:
                    token = await mint_on_slot(slot, site_key, page_url, timeout, ua)
                    slot.fail_streak = 0
                    return {"id": rid, "ok": True, "token": token}
                except Exception as exc:
                    slot.fail_streak += 1
                    if slot.fail_streak >= 2:
                        try:
                            await slot.reset()
                            slot.fail_streak = 0
                        except Exception:
                            pass
                    return {"id": rid, "ok": False, "error": f"{type(exc).__name__}: {exc}"}
        finally:
            self.queue.put_nowait(slot)

    async def close(self) -> None:
        await asyncio.gather(*(s.close() for s in self.slots))


async def run_pool(size: int, chrome: str, proxy: str, mode: str = "offscreen") -> None:
    pool = Pool(size, chrome, proxy, mode=mode)
    try:
        await pool.warm()
        emit({"ok": True, "event": "ready", "workers": size, "chrome": chrome})
        loop = asyncio.get_event_loop()
        reader = asyncio.StreamReader()
        protocol = asyncio.StreamReaderProtocol(reader)
        await loop.connect_read_pipe(lambda: protocol, sys.stdin)

        pending: set[asyncio.Task] = set()

        async def handle_line(line: str) -> None:
            line = line.strip()
            if not line:
                return
            try:
                req = json.loads(line)
            except Exception as exc:
                emit({"ok": False, "error": f"bad json: {exc}"})
                return
            if req.get("cmd") == "ping":
                emit({"ok": True, "event": "pong", "id": req.get("id")})
                return
            if req.get("cmd") == "shutdown":
                emit({"ok": True, "event": "bye"})
                raise SystemExit(0)
            res = await pool.mint(req)
            emit(res)

        while True:
            raw = await reader.readline()
            if not raw:
                break
            line = raw.decode("utf-8", "replace")
            task = asyncio.create_task(handle_line(line))
            pending.add(task)
            task.add_done_callback(pending.discard)
        if pending:
            await asyncio.gather(*pending, return_exceptions=True)
    finally:
        await pool.close()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--workers", type=int, default=2)
    ap.add_argument("--proxy", default="")
    ap.add_argument("--chrome", default="")
    ap.add_argument(
        "--mode",
        default="offscreen",
        choices=("offscreen", "headless", "auto"),
        help="offscreen=headed off-display (default); headless=true headless",
    )
    args = ap.parse_args()
    chrome = args.chrome.strip() or find_chrome()
    if not chrome:
        emit({"ok": False, "error": "chrome not found"})
        return 1
    proxy = args.proxy.strip() or (os.environ.get("REGISTER_PROXY") or "").strip()
    mode = (args.mode or "offscreen").strip().lower()
    try:
        asyncio.run(run_pool(max(1, args.workers), chrome, proxy, mode=mode))
    except SystemExit:
        return 0
    except Exception as exc:
        emit({"ok": False, "error": f"{type(exc).__name__}: {exc}"})
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
