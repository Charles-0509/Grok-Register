#!/usr/bin/env python3
"""Complete x.ai OAuth device consent in a real browser (Playwright).

New accounts often get invalid_grant after pure HTTP approve even when
the server returns /device/done. This script mimics a user: open
verification_uri_complete, inject sso cookie, click Allow, wait for done.

Usage:
  oauth_device_approve.py --url URL --sso JWT [--proxy URL] [--chrome PATH]
                          [--timeout 90] [--mode offscreen|headless|auto]

Exit 0 and print "ok" on success; otherwise exit 1 with error on stderr.
"""
from __future__ import annotations

import argparse
import asyncio
import glob
import os
import sys
import time


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
        "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    ):
        if os.path.exists(p):
            return p
    return ""


def has_display() -> bool:
    return bool(
        (os.environ.get("DISPLAY") or "").strip()
        or (os.environ.get("WAYLAND_DISPLAY") or "").strip()
    )


def resolve_launch_mode(mode: str) -> tuple[str, bool]:
    mode = (mode or "offscreen").strip().lower()
    if mode in ("", "auto"):
        mode = "offscreen"
    if mode == "headless":
        return "headless", True
    if has_display():
        return "offscreen", False
    print(
        "warn: no $DISPLAY; headless fallback for device approve",
        file=sys.stderr,
    )
    return "headless-no-display", True


def launch_args(label: str) -> list[str]:
    args = [
        "--no-sandbox",
        "--disable-blink-features=AutomationControlled",
        "--no-first-run",
        "--no-default-browser-check",
        "--disable-infobars",
        "--disable-dev-shm-usage",
    ]
    if label == "offscreen":
        args.extend(
            [
                "--window-position=-32000,-32000",
                "--window-size=1100,800",
            ]
        )
    return args


async def approve(
    url: str,
    sso: str,
    proxy: str,
    chrome: str,
    timeout: float,
    mode: str,
    ua: str,
) -> None:
    from playwright.async_api import async_playwright

    label, use_headless = resolve_launch_mode(mode)
    launch: dict = {
        "executable_path": chrome,
        "headless": use_headless,
        "args": launch_args(label),
    }
    if proxy:
        launch["proxy"] = {"server": proxy}

    deadline = time.time() + max(30.0, timeout)

    async with async_playwright() as pw:
        browser = await pw.chromium.launch(**launch)
        try:
            ctx_kwargs: dict = {
                "viewport": {"width": 1100, "height": 800},
                "locale": "en-US",
            }
            if ua:
                ctx_kwargs["user_agent"] = ua
            context = await browser.new_context(**ctx_kwargs)
            await context.add_init_script(
                'Object.defineProperty(navigator,"webdriver",{get:()=>undefined})'
            )
            # SSO for accounts + auth hosts
            for domain in (".x.ai", "accounts.x.ai", "auth.x.ai"):
                try:
                    await context.add_cookies(
                        [
                            {
                                "name": "sso",
                                "value": sso,
                                "domain": domain if domain.startswith(".") else domain,
                                "path": "/",
                            }
                        ]
                    )
                except Exception:
                    try:
                        await context.add_cookies(
                            [
                                {
                                    "name": "sso",
                                    "value": sso,
                                    "url": "https://accounts.x.ai/",
                                    "path": "/",
                                }
                            ]
                        )
                    except Exception as e:
                        print(f"cookie set warn: {e}", file=sys.stderr)

            page = await context.new_page()
            page.set_default_timeout(45000)

            print(f"goto {url} mode={label}", file=sys.stderr)
            await page.goto(url, wait_until="domcontentloaded", timeout=60000)
            await page.wait_for_timeout(1200)

            # If landed on sign-in, cookie may not be accepted
            cur = page.url
            if "sign-in" in cur or "login" in cur.lower():
                raise RuntimeError(f"landed_on_sign_in url={cur}")

            async def page_says_done() -> bool:
                try:
                    u = page.url
                    if "/oauth2/device/done" in u and "denied" not in u:
                        return True
                    txt = (await page.inner_text("body")).lower()
                    if "device authorized" in txt or "you have authorized" in txt:
                        return True
                    if "设备已授权" in txt:
                        return True
                except Exception:
                    pass
                return False

            if await page_says_done():
                print("ok", flush=True)
                return

            # Click Allow — prefer button with text Allow / 允许
            clicked = False
            for selector in (
                'button:has-text("Allow")',
                'button:has-text("允许")',
                'form[action*="device/approve"] button[type="submit"]',
                'button[type="submit"]',
            ):
                try:
                    loc = page.locator(selector)
                    n = await loc.count()
                    if n == 0:
                        continue
                    # Prefer last submit if multiple (Deny then Allow often)
                    target = loc.nth(n - 1) if n > 1 and "submit" in selector else loc.first
                    text = (await target.inner_text()).strip().lower()
                    if "deny" in text or "拒绝" in text:
                        if n > 1:
                            target = loc.nth(n - 1)
                            text = (await target.inner_text()).strip().lower()
                        if "deny" in text or "拒绝" in text:
                            continue
                    await target.click(timeout=8000)
                    clicked = True
                    print(f"clicked {selector!r} text={text!r}", file=sys.stderr)
                    break
                except Exception as e:
                    print(f"click try {selector}: {e}", file=sys.stderr)

            if not clicked:
                # Fallback: set hidden action=allow and submit form via JS
                try:
                    await page.evaluate(
                        """() => {
                        const f = document.querySelector('form[action*="device/approve"]')
                          || document.querySelector('form');
                        if (!f) return false;
                        let a = f.querySelector('input[name="action"]');
                        if (!a) {
                          a = document.createElement('input');
                          a.type = 'hidden'; a.name = 'action'; f.appendChild(a);
                        }
                        a.value = 'allow';
                        f.submit();
                        return true;
                    }"""
                    )
                    clicked = True
                    print("form submit via JS action=allow", file=sys.stderr)
                except Exception as e:
                    raise RuntimeError(f"no_allow_button: {e}") from e

            # Wait for done
            while time.time() < deadline:
                if await page_says_done():
                    print("ok", flush=True)
                    return
                # follow soft navigations
                await page.wait_for_timeout(500)

            # dump hint
            try:
                body = (await page.inner_text("body"))[:300].replace("\n", " ")
            except Exception:
                body = ""
            raise RuntimeError(
                f"timeout waiting device done url={page.url} body={body!r}"
            )
        finally:
            await browser.close()


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--url", required=True, help="verification_uri_complete")
    ap.add_argument("--sso", required=True, help="session JWT")
    ap.add_argument("--proxy", default="")
    ap.add_argument("--chrome", default="")
    ap.add_argument("--timeout", type=float, default=90)
    ap.add_argument("--mode", default=os.environ.get("TURNSTILE_MODE", "offscreen"))
    ap.add_argument("--ua", default="")
    args = ap.parse_args()

    sso = (args.sso or "").strip()
    if not sso:
        print("empty sso", file=sys.stderr)
        return 1
    chrome = (args.chrome or "").strip() or find_chrome()
    if not chrome:
        print("chrome not found; set CHROME_PATH", file=sys.stderr)
        return 1

    try:
        asyncio.run(
            approve(
                url=args.url.strip(),
                sso=sso,
                proxy=(args.proxy or "").strip(),
                chrome=chrome,
                timeout=args.timeout,
                mode=args.mode,
                ua=(args.ua or "").strip(),
            )
        )
        return 0
    except Exception as e:
        print(f"device_approve_browser: {e}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
