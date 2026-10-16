PR_WATCH := $(HOME)/.claude/pr-watch/pr-watch
TABS_PLUGIN := $(HOME)/.config/zellij/plugins/pr-watch-tabs.wasm

.PHONY: build test install audit

build:
	go build -o bin/pr-watch ./pr-watch
	cd zellij-tabs && cargo build --release --target wasm32-wasip1

test:
	go vet ./...
	go test ./...
	hooks/pr-guard_test.sh

# pr-watch is copied (a running TUI keeps its old binary) and linked onto PATH; cc-status and pr-guard are symlinked
# so edits here are live
install: build
	install -d $(dir $(PR_WATCH)) $(HOME)/.local/bin $(HOME)/.claude/hooks $(dir $(TABS_PLUGIN))
	install -m 755 bin/pr-watch $(PR_WATCH)
	install -m 644 zellij-tabs/target/wasm32-wasip1/release/pr-watch-tabs.wasm $(TABS_PLUGIN)
	ln -sf $(PR_WATCH) $(HOME)/.local/bin/pr-watch
	ln -sf $(CURDIR)/cc-status/cc-status $(HOME)/.local/bin/cc-status
	ln -sf $(CURDIR)/hooks/pr-guard.sh $(HOME)/.claude/hooks/pr-guard.sh

# run before publishing: private words anywhere in history, then a secret scan of every commit
audit:
	scripts/check-private.sh --history
	go run github.com/zricethezav/gitleaks/v8@latest git --log-opts=--all --redact .
