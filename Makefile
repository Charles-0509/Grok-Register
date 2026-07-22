APP=grok
MODULE=github.com/grok-free-register/grok-reg
VERSION?=0.1.0
PREFIX?=/usr/local
BINDIR=$(PREFIX)/bin

.PHONY: build install uninstall clean test run

# Resolve go even when sudo drops PATH (common: /usr/local/go/bin).
GO ?= $(shell command -v go 2>/dev/null || true)
ifeq ($(GO),)
  GO := $(firstword $(wildcard /usr/local/go/bin/go /usr/lib/go*/bin/go $(HOME)/go/bin/go $(HOME)/.local/go/bin/go))
endif

build:
	@if [ -z "$(GO)" ] || [ ! -x "$(GO)" ]; then \
		echo "错误: 找不到 go。请安装 Go 1.21+ 或把 go 加入 PATH。"; \
		echo "  例: export PATH=\$$PATH:/usr/local/go/bin"; \
		exit 1; \
	fi
	$(GO) build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/$(APP) ./cmd/grok

# 不强制 rebuild：已有 bin/grok 时直接安装（避免 sudo 丢 PATH 再编一次失败）
install:
	@if [ ! -x bin/$(APP) ]; then \
		echo "[*] bin/$(APP) 不存在，先 build..."; \
		$(MAKE) build; \
	fi
	install -d $(BINDIR)
	install -m 755 bin/$(APP) $(BINDIR)/$(APP)
	# Playwright mint helpers (Turnstile) — one-shot + persistent pool
	install -d /usr/local/share/grok-reg
	install -m 755 scripts/turnstile_mint.py /usr/local/share/grok-reg/turnstile_mint.py
	install -m 755 scripts/turnstile_pool.py /usr/local/share/grok-reg/turnstile_pool.py
	@echo "installed: $(BINDIR)/$(APP)"
	@echo "installed: /usr/local/share/grok-reg/turnstile_mint.py"
	@echo "installed: /usr/local/share/grok-reg/turnstile_pool.py"
	@echo "try: grok help"
	@echo "Turnstile: pip install -r scripts/requirements-turnstile.txt && python -m cloakbrowser install"

uninstall:
	rm -f $(BINDIR)/$(APP)

clean:
	rm -rf bin/

test:
	@if [ -z "$(GO)" ] || [ ! -x "$(GO)" ]; then echo "go not found"; exit 1; fi
	$(GO) test ./...

run:
	@if [ -z "$(GO)" ] || [ ! -x "$(GO)" ]; then echo "go not found"; exit 1; fi
	$(GO) run ./cmd/grok help
