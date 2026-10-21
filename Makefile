SHELL := /bin/bash
.DEFAULT_GOAL := help
.DELETE_ON_ERROR:

GOCC ?= go
GOFMT ?= gofmt
GOTOOLCHAIN ?= local
export GOTOOLCHAIN
export GOWORK := $(CURDIR)/go.work

PKG := github.com/GeorgeTsagk/L3D
BIN_DIR := $(CURDIR)/bin
BTCD_DIR := $(CURDIR)/.deps/btcd
PREFIX ?= $(shell $(GOCC) env GOPATH)
DESTDIR ?=
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo development)
VERSION := 0.1.0-regtest
BUILD_FLAGS := -trimpath -tags=layertree
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT)
GOBUILD := $(GOCC) build $(BUILD_FLAGS)
GOTEST := $(GOCC) test -tags=layertree
TEST_FLAGS ?= -count=1 -timeout=5m
PKGS ?= ./cmd/... ./lite ./operator ./protocol ./tree ./script ./txn ./msg
CASE ?= .
ITEST ?= TestRegtest
RACE ?=
RUN_DIR ?= $(CURDIR)/runs/$(shell date -u +%Y%m%dT%H%M%S)-$(shell mktemp -u XXXXXX)
BTCD_CONFIG ?= $(CURDIR)/btcd.conf
GO_DIRS := cmd lite operator protocol tree script txn msg mvp

.PHONY: help all deps build client backend build-itest install unit unit-race \
	backend-test itest backend-guard setup-test test check lint fmt fmt-check demo \
	demo-lite demo-protocol modes run-backend clean clean-deps

help: ## List build, test and regtest commands.
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

all: build ## Build the client, demo and patched btcd.

deps: ## Fetch the pinned btcd source and apply the checked patch.
	@./scripts/bootstrap-btcd.sh

client: deps ## Build bin/l3d.
	@mkdir -p "$(BIN_DIR)"
	$(GOBUILD) -ldflags "$(LDFLAGS)" -o "$(BIN_DIR)/l3d" ./cmd/l3d

backend: deps ## Build bin/btcd with private regtest opcode support.
	@mkdir -p "$(BIN_DIR)"
	cd "$(BTCD_DIR)" && $(GOCC) build -trimpath -o "$(BIN_DIR)/btcd" .

build-itest: client backend ## Build binaries used by the real-node integration tests.
	$(GOBUILD) -o "$(BIN_DIR)/l3d-demo" ./cmd/l3d-demo

build: build-itest ## Build all three binaries.

install: client ## Install l3d to PREFIX/bin. Defaults to GOPATH/bin.
	install -d "$(DESTDIR)$(PREFIX)/bin"
	install -m 0755 "$(BIN_DIR)/l3d" "$(DESTDIR)$(PREFIX)/bin/l3d"

unit: deps ## Run unit and native-script tests. Set PKGS and CASE to filter.
	$(GOTEST) $(RACE) $(TEST_FLAGS) -run '$(CASE)' $(PKGS)

unit-race: ## Run the unit suite with the race detector.
	$(MAKE) unit RACE=-race

backend-test: deps ## Test the changed btcd interpreter and validation packages.
	cd "$(BTCD_DIR)" && $(GOCC) test $(RACE) $(TEST_FLAGS) ./txscript ./blockchain ./mempool ./mining

backend-guard: backend ## Assert that the experimental flag fails outside regtest.
	@./scripts/check-regtest.sh "$(BIN_DIR)/btcd"

itest: build-itest backend-guard ## Run real-node tests. Set ITEST to filter.
	LAYERTREE_BTCD="$(BIN_DIR)/btcd" LAYERTREE_LITECLI="$(BIN_DIR)/l3d" \
		$(GOTEST) $(RACE) $(TEST_FLAGS) -run '$(ITEST)' ./mvp

setup-test: deps ## Test repeatable backend setup and refusal of modified sources.
	@./scripts/test-bootstrap.sh

test: setup-test unit backend-test itest ## Run setup, Go and regtest tests.

lint: deps fmt-check ## Check formatting and run go vet.
	$(GOCC) vet -tags=layertree ./...

check: lint test ## Run the checks used by CI.

fmt: ## Format the Go sources.
	@find $(GO_DIRS) -name '*.go' -type f -exec $(GOFMT) -w {} +

fmt-check: ## Fail if Go sources need formatting.
	@files=$$(find $(GO_DIRS) -name '*.go' -type f -exec $(GOFMT) -l {} +); \
		if [ -n "$$files" ]; then printf '%s\n' "$$files"; exit 1; fi

demo: build ## Demonstrate all nine configurations on one regtest node.
	"$(BIN_DIR)/l3d-demo" -btcd "$(BIN_DIR)/btcd" -lite-client "$(BIN_DIR)/l3d" -all-modes -out "$(RUN_DIR)"

demo-lite: build ## Demonstrate the original dense MID offline-client scenario.
	"$(BIN_DIR)/l3d-demo" -btcd "$(BIN_DIR)/btcd" -lite-client "$(BIN_DIR)/l3d" -out "$(RUN_DIR)"

demo-protocol: build ## Demonstrate the dense MID protocol transitions.
	"$(BIN_DIR)/l3d-demo" -btcd "$(BIN_DIR)/btcd" -out "$(RUN_DIR)"

modes: client ## Print the supported tier and profile combinations as JSON.
	@"$(BIN_DIR)/l3d" modes

run-backend: backend ## Run btcd in the foreground using BTCD_CONFIG.
	@test -f "$(BTCD_CONFIG)" || { echo 'Copy sample-btcd.conf to btcd.conf first.' >&2; exit 1; }
	"$(BIN_DIR)/btcd" --configfile="$(BTCD_CONFIG)"

clean: ## Remove binaries. Preserve member state and regtest data.
	rm -rf "$(BIN_DIR)"

clean-deps: ## Remove the downloaded backend source.
	rm -rf "$(BTCD_DIR)"
