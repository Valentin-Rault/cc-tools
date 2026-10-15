PR_WATCH := $(HOME)/.claude/pr-watch/pr-watch

.PHONY: build test install audit

build:
	go build -o bin/pr-watch ./pr-watch

test:
	go vet ./...
	go test ./...
	hooks/pr-guard_test.sh

# pr-watch is copied (a running TUI keeps its old binary); cc-status and pr-guard are symlinked so edits here are live
install: build
	install -d $(dir $(PR_WATCH)) $(HOME)/.local/bin $(HOME)/.claude/hooks
	install -m 755 bin/pr-watch $(PR_WATCH)
	ln -sf $(CURDIR)/cc-status/cc-status $(HOME)/.local/bin/cc-status
	ln -sf $(CURDIR)/hooks/pr-guard.sh $(HOME)/.claude/hooks/pr-guard.sh

# run before publishing: private words anywhere in history, then a secret scan of every commit
audit:
	scripts/check-private.sh --history
	go run github.com/zricethezav/gitleaks/v8@latest git --log-opts=--all --redact .
