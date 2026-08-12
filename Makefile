# dbgraph developer Makefile.
#
# `make run` and `make watch` serve plain HTTP on a loopback address and pass
# --insecure-cleartext-web, so the Web UI, health, and MCP all work without a
# certificate. Add TLS=1 for HTTPS with a generated self-signed certificate,
# and MYSQL_TLS=0 to scan a source MySQL that has no certificate. Generated
# development credentials live under $(LOCAL_DIR) and stay out of git.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c
.DEFAULT_GOAL := help

GO ?= go
NPM ?= npm
WEB_DIR ?= web
CONSOLE_DIR ?= internal/transport/webapi/app
BIN_DIR ?= bin
BINARY ?= $(BIN_DIR)/dbgraph
CMD_PACKAGE ?= ./cmd/dbgraph

LOCAL_DIR ?= .dbgraph-local
ENV_FILE ?= $(LOCAL_DIR)/dev.env
TLS_CERT ?= $(LOCAL_DIR)/cert.pem
TLS_KEY ?= $(LOCAL_DIR)/key.pem
CERT_DAYS ?= 30

DATABASE ?= ./dbgraph.sqlite
LISTEN ?= 127.0.0.1:8080
COVERAGE_FILE ?= coverage.out

# Rebuild inputs: non-test Go sources, embedded migrations, embedded Web assets.
WATCH_PATHS := cmd internal migrations
SOURCES := $(shell find $(WATCH_PATHS) -type f ! -name '*_test.go' \
	-not -path '$(CONSOLE_DIR)/*' \( -name '*.go' -o -name '*.sql' \) 2>/dev/null)
WATCH_INTERVAL ?= 1

# TLS=0 (default) serves cleartext HTTP; TLS=1 serves HTTPS with $(TLS_CERT).
TLS ?= 0
# MYSQL_TLS=0 lets schema scans reach a source MySQL that has no certificate.
MYSQL_TLS ?= 1
ifeq ($(MYSQL_TLS),0)
MYSQL_FLAGS := --insecure-mysql-tls
else
MYSQL_FLAGS :=
endif

# Read from the file rather than the environment, and group by endpoint, so the
# banner always says which token belongs in which place.
SHOW_MCP_TOKENS = awk -F= '/^DBGRAPH_MCP_TOKEN=/ {printf "    %-28s %s\n", $$1, $$2}' $(ENV_FILE)
SHOW_WEB_TOKENS = awk -F= '/^DBGRAPH_WEB_TOKEN=/ {printf "    %-28s %s\n", $$1, $$2}' $(ENV_FILE)

ifeq ($(TLS),0)
SCHEME := http
# Cleartext Web sign-in is loopback-only and refuses to start otherwise.
TLS_FLAGS := --insecure-cleartext-web
TLS_DEPS :=
MCP_TLS_ENV :=
LOAD_ENV := set -a; . ./$(ENV_FILE); set +a;
BANNER = printf 'Tokens (%s)\n' '$(ENV_FILE)'; \
	printf '  Web sign-in at http://%s/ - paste one of these\n' '$(LISTEN)'; \
	$(SHOW_WEB_TOKENS); \
	printf '  MCP bearer tokens for http://%s/mcp\n' '$(LISTEN)'; \
	$(SHOW_MCP_TOKENS); \
	printf '  Secret key for stored DSNs\n'; \
	awk -F= '/^DBGRAPH_SECRET_KEY=/ {printf "    %-28s %s\n", $$1, $$2}' $(ENV_FILE); \
	printf 'Health: http://%s/healthz\n' '$(LISTEN)'; \
	printf 'Cleartext mode: sessions and tokens are sent unencrypted on\n'; \
	printf 'loopback. Use TLS=1 for anything reachable by another host.\n\n'
else
SCHEME := https
TLS_FLAGS := --tls-cert $(TLS_CERT) --tls-key $(TLS_KEY)
TLS_DEPS := $(TLS_CERT)
MCP_TLS_ENV := SSL_CERT_FILE=$(TLS_CERT)
LOAD_ENV := set -a; . ./$(ENV_FILE); set +a;
BANNER = printf 'Tokens (%s)\n' '$(ENV_FILE)'; \
	printf '  Web sign-in at https://%s/ - paste one of these\n' '$(LISTEN)'; \
	$(SHOW_WEB_TOKENS); \
	printf '  MCP bearer tokens for https://%s/mcp\n' '$(LISTEN)'; \
	$(SHOW_MCP_TOKENS); \
	printf 'Health: https://%s/healthz\n\n' '$(LISTEN)'
endif

SERVER_URL ?= $(SCHEME)://$(LISTEN)
SERVE_FLAGS := serve --database $(DATABASE) --listen $(LISTEN) $(TLS_FLAGS) $(MYSQL_FLAGS)

.PHONY: help build console run watch dev-env ensure-secret-key certs tokens rotate-tokens rotate-certs \
	test test-race vet fmt lint verify cover tidy mcp clean

help: ## Show the available targets
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "} {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

build: console $(BINARY) ## Build the console and ./bin/dbgraph

console: $(CONSOLE_DIR)/index.html ## Build the Vue console into the embedded directory

# The build output is git-ignored, so a fresh checkout has to produce it before
# the binary can embed anything useful.
$(CONSOLE_DIR)/index.html: $(shell find $(WEB_DIR)/src $(WEB_DIR)/index.html -type f 2>/dev/null) $(WEB_DIR)/package.json
	@command -v $(NPM) >/dev/null || { echo 'npm is required to build the console' >&2; exit 1; }
	@[ -d $(WEB_DIR)/node_modules ] || (cd $(WEB_DIR) && $(NPM) install)
	cd $(WEB_DIR) && $(NPM) run build

$(BINARY): $(SOURCES) go.mod go.sum $(CONSOLE_DIR)/index.html
	$(GO) build -o $(BINARY) $(CMD_PACKAGE)

run: build dev-env ## Build and serve locally over HTTP (TLS=1 for HTTPS)
	@$(LOAD_ENV) \
	$(BANNER); \
	exec $(BINARY) $(SERVE_FLAGS)

watch: dev-env ## Rebuild and restart the server whenever sources change
	@$(LOAD_ENV) \
	$(BANNER); \
	server_pid=""; \
	stop_server() { \
		if [ -n "$$server_pid" ] && kill -0 "$$server_pid" 2>/dev/null; then \
			kill -TERM "$$server_pid" 2>/dev/null || true; \
			wait "$$server_pid" 2>/dev/null || true; \
		fi; \
		server_pid=""; \
	}; \
	fingerprint() { \
		{ find $(WEB_DIR)/src $(WEB_DIR)/index.html -type f -printf '%T@ %p\n' 2>/dev/null || true; \
		  find $(WATCH_PATHS) -type f ! -name '*_test.go' \
			-not -path '$(CONSOLE_DIR)/*' \
			\( -name '*.go' -o -name '*.sql' \) \
			-printf '%T@ %p\n' 2>/dev/null || true; \
		  find go.mod go.sum -printf '%T@ %p\n' 2>/dev/null || true; } \
		| sort | cksum; \
	}; \
	rebuild() { \
		printf '\n[watch] building\n'; \
		if (cd $(WEB_DIR) && $(NPM) run build >/dev/null 2>&1) && $(GO) build -o '$(BINARY).next' $(CMD_PACKAGE); then \
			stop_server; \
			mv -f '$(BINARY).next' '$(BINARY)'; \
			printf '[watch] serving %s on %s\n' '$(BINARY)' '$(LISTEN)'; \
			$(BINARY) $(SERVE_FLAGS) & \
			server_pid=$$!; \
		else \
			printf '[watch] build failed; keeping the running process\n'; \
			rm -f '$(BINARY).next'; \
		fi; \
	}; \
	trap 'stop_server; rm -f "$(BINARY).next"; exit 0' INT TERM; \
	install -d -m 755 $(BIN_DIR); \
	previous=$$(fingerprint); \
	rebuild; \
	while :; do \
		sleep $(WATCH_INTERVAL); \
		current=$$(fingerprint); \
		if [ "$$current" != "$$previous" ]; then \
			previous="$$current"; \
			rebuild; \
		elif [ -n "$$server_pid" ] && ! kill -0 "$$server_pid" 2>/dev/null; then \
			wait "$$server_pid" 2>/dev/null || true; \
			server_pid=""; \
			printf '[watch] server exited; waiting for the next change\n'; \
		fi; \
	done

dev-env: $(ENV_FILE) ensure-secret-key $(TLS_DEPS) ## Create the development tokens (and the certificate when TLS=1)

# Adds only what is missing, so an env file generated before stored DSNs
# existed gains a key without its tokens changing.
ensure-secret-key: $(ENV_FILE)
	@grep -q '^DBGRAPH_SECRET_KEY=' $(ENV_FILE) || { \
		umask 077; printf 'DBGRAPH_SECRET_KEY=%s\n' "$$(openssl rand -hex 32)" >> $(ENV_FILE); \
		printf 'Added DBGRAPH_SECRET_KEY to %s\n' '$(ENV_FILE)'; \
	}

certs: $(TLS_CERT) ## Generate the local self-signed certificate

$(TLS_CERT):
	@command -v openssl >/dev/null || { echo 'openssl is required to generate the local certificate' >&2; exit 1; }
	install -d -m 700 $(LOCAL_DIR)
	openssl req -x509 -newkey rsa:3072 -sha256 -days $(CERT_DAYS) -nodes \
		-keyout $(TLS_KEY) -out $(TLS_CERT) \
		-subj '/CN=localhost' -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1'
	chmod 600 $(TLS_KEY) $(TLS_CERT)

# Written once and never regenerated: the tokens stay stable until you run
# `make rotate-tokens` or delete the file yourself. `make clean` leaves it alone.
$(ENV_FILE):
	@command -v openssl >/dev/null || { echo 'openssl is required to generate development tokens' >&2; exit 1; }
	install -d -m 700 $(LOCAL_DIR)
	umask 077; { \
		echo '# Local development credentials generated by make dev-env. Never reuse these.'; \
		for name in DBGRAPH_WEB_TOKEN DBGRAPH_MCP_TOKEN DBGRAPH_SECRET_KEY; do \
			printf '%s=%s\n' "$$name" "$$(openssl rand -hex 32)"; \
		done; \
	} > $(ENV_FILE)

tokens: dev-env ## Print the generated development tokens
	@cat $(ENV_FILE)

rotate-tokens: ## Replace the development tokens (invalidates the current ones)
	@printf 'Replacing %s. Existing tokens and sessions stop working.\n' '$(ENV_FILE)'
	rm -f $(ENV_FILE)
	@$(MAKE) --no-print-directory $(ENV_FILE)
	@cat $(ENV_FILE)

rotate-certs: ## Replace the development TLS certificate
	rm -f $(TLS_CERT) $(TLS_KEY)
	@$(MAKE) --no-print-directory $(TLS_CERT)

mcp: build dev-env ## Run the stdio MCP proxy against a local server
	@$(LOAD_ENV) \
	$(MCP_TLS_ENV) \
		exec $(BINARY) mcp --server-url $(SERVER_URL)

test: ## Run the test suite
	$(GO) test ./...

test-race: ## Run the test suite with the race detector
	$(GO) test -race ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Format the Go sources
	gofmt -l -w $(WATCH_PATHS) tests

lint: ## Run staticcheck and golangci-lint when installed
	@if command -v staticcheck >/dev/null; then staticcheck ./...; \
	else echo 'staticcheck is not installed; skipping'; fi
	@if command -v golangci-lint >/dev/null; then golangci-lint run; \
	else echo 'golangci-lint is not installed; skipping'; fi

verify: fmt test test-race vet lint ## Run the pre-handoff verification gates

cover: ## Report total test coverage
	$(GO) test -coverprofile=$(COVERAGE_FILE) ./...
	$(GO) tool cover -func=$(COVERAGE_FILE) | tail -1

tidy: ## Tidy the module requirements
	$(GO) mod tidy

clean: ## Remove build and coverage artifacts
	rm -rf $(BIN_DIR) $(COVERAGE_FILE)
