# syntax=docker/dockerfile:1.6
# Multi-stage build for Grok-Register on Windows / Docker Desktop.
#
# Stage 1: compile the Go CLI on linux/amd64 (uses Unix syscalls — fine in-container).
# Stage 2: runtime image with Python + Playwright + CloakBrowser Chromium + the grok binary.
#
# Image runs as root (matches upstream turnstile_mint.py assumptions and
# ~/.cloakbrowser install path). Don't enable rootless mode for this image.

ARG GO_VERSION=1.26
ARG PYTHON_VERSION=3.12
ARG DEBIAN=bookworm

# ---------- Stage 1: build grok binary ----------
FROM golang:${GO_VERSION}-${DEBIAN} AS builder

ARG MODULE=github.com/grok-free-register/grok-reg
ARG VERSION=0.1.0

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# Static-ish build; cgo not required by this codebase.
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/grok ./cmd/grok

# ---------- Stage 2: runtime ----------
FROM python:${PYTHON_VERSION}-slim-${DEBIAN} AS runtime

# Playwright/CloakBrowser Chromium runtime libs (must match playwright install list).
# Keep aligned with upstream: mcr.microsoft.com/playwright/python deps for Debian bookworm.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl gnupg tini \
      fonts-liberation fonts-noto-cjk \
      libnss3 libnspr4 libatk1.0-0 libatk-bridge2.0-0 libcups2 \
      libdrm2 libxkbcommon0 libxcomposite1 libxdamage1 libxfixes3 \
      libxrandr2 libgbm1 libasound2 libpango-1.0-0 libcairo2 \
      libxshmfence1 libx11-xcb1 libxcb-dri3-0 libxss1 \
      procps \
    && rm -rf /var/lib/apt/lists/*

# Independent venv at a fixed path (matches the path baked into playwright_bridge.go).
RUN python3 -m venv /opt/cloakbrowser-venv \
    && /opt/cloakbrowser-venv/bin/pip install -U pip

# Pull scripts/requirements-turnstile.txt from repo and install into the venv.
COPY scripts/requirements-turnstile.txt /tmp/req.txt
RUN /opt/cloakbrowser-venv/bin/pip install -r /tmp/req.txt && rm /tmp/req.txt

# Download CloakBrowser Chromium into /root/.cloakbrowser (root home in container).
RUN /opt/cloakbrowser-venv/bin/python -m cloakbrowser install

# Install grok binary + turnstile mint helper.
COPY --from=builder /out/grok /usr/local/bin/grok
COPY scripts/turnstile_mint.py /usr/local/share/grok-reg/turnstile_mint.py

# Default env (override via docker-compose / -e).
ENV GROK_HOME=/data \
    GROK_PYTHON=/opt/cloakbrowser-venv/bin/python \
    GROK_TURNSTILE_SCRIPT=/usr/local/share/grok-reg/turnstile_mint.py \
    CLOAKBROWSER_SUPPRESS_FONT_WARNING=1 \
    PYTHONUNBUFFERED=1

VOLUME ["/data"]
WORKDIR /data

# tini reaps zombies so worker forks don't leak; signal handling stays POSIX-correct.
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/docker-entrypoint.sh"]

COPY docker/entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# Default mode = idle (long-running container user can `docker exec` into).
# Set MODE=run + GROK_TARGET=N to run one batch in the foreground.
CMD ["idle"]