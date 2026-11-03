PR_WATCH := $(HOME)/.claude/pr-watch/pr-watch
CC_STATUS := $(HOME)/.claude/cc-status/cc-status
TABS_PLUGIN := $(HOME)/.config/zellij/plugins/pr-watch-tabs.wasm

.PHONY: build test install audit

WASM := zellij-tabs/target/wasm32-wasip1/release/pr-watch-tabs.wasm

# the zellij plugin only where there's cargo: tmux, or no multiplexer, doesn't need it
build:
	go build -o bin/pr-watch ./pr-watch
	go build -o bin/cc-status ./cc-status
	@if command -v cargo >/dev/null; then cd zellij-tabs && cargo build --release --target wasm32-wasip1; \
	else echo "no cargo: skipping the zellij plugin (pr-watch's tabs by stage need it in zellij, not in tmux)"; fi

test:
	go vet ./...
	go test ./...
	hooks/pr-guard_test.sh

# pr-watch and cc-status are copied (what runs keeps its old binary) and linked onto PATH; pr-guard is symlinked so
# edits here are live.
install: build
	install -d $(dir $(PR_WATCH)) $(dir $(CC_STATUS)) $(HOME)/.local/bin $(HOME)/.claude/hooks
	install -m 755 bin/pr-watch $(PR_WATCH)
	install -m 755 bin/cc-status $(CC_STATUS)
	@if [ -f $(WASM) ]; then install -d $(dir $(TABS_PLUGIN)) && install -m 644 $(WASM) $(TABS_PLUGIN); fi
	ln -sf $(PR_WATCH) $(HOME)/.local/bin/pr-watch
	ln -sf $(CC_STATUS) $(HOME)/.local/bin/cc-status
	ln -sf $(CURDIR)/hooks/pr-guard.sh $(HOME)/.claude/hooks/pr-guard.sh

# run before publishing: private words anywhere in history, then a secret scan of every commit
audit:
	scripts/check-private.sh --history
	go run github.com/zricethezav/gitleaks/v8@latest git --log-opts=--all --redact .
