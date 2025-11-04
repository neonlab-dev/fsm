GO ?= go
GOCACHE_DIR := $(CURDIR)/.gocache
GOMODCACHE_DIR := $(CURDIR)/.gomodcache

export GOPROXY ?= off
export GOSUMDB ?= off
export GOTOOLCHAIN ?= local

.PHONY: test coverage clean-cache

test:
	@echo ">> running unit tests"
	GOCACHE=$(GOCACHE_DIR) GOMODCACHE=$(GOMODCACHE_DIR) $(GO) test ./...

coverage:
	@echo ">> generating coverage.out"
	GOCACHE=$(GOCACHE_DIR) GOMODCACHE=$(GOMODCACHE_DIR) $(GO) test -coverprofile=coverage.out ./...

clean-cache:
	@echo ">> removing go build caches"
	rm -rf $(GOCACHE_DIR)
