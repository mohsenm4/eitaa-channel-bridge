BINARY      := bin/bridge
PKG         := ./cmd/bridge
IMAGE       := eitaa-channel-bridge:latest
COMPOSE     := docker compose
SERVICE     := eitaa-bridge

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Targets:\n"} \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

# ── native Go ──────────────────────────────────────────────────────

.PHONY: build
build: ## Compile the bridge binary into bin/
	@mkdir -p bin
	go build -trimpath -ldflags="-s -w" -o $(BINARY) $(PKG)

.PHONY: run
run: ## Run `bridge run` against local .env
	go run $(PKG) run

.PHONY: dump
dump: ## Run `bridge dump` against local .env
	go run $(PKG) dump

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: vet
vet: ## go vet all packages
	go vet ./...

.PHONY: fmt
fmt: ## gofmt on the whole tree
	gofmt -s -w .

.PHONY: tidy
tidy: ## go mod tidy
	go mod tidy

.PHONY: check
check: fmt vet test ## fmt + vet + test

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin

# ── Docker ─────────────────────────────────────────────────────────

.PHONY: docker-build
docker-build: ## Build the bridge image
	docker build -t $(IMAGE) .

.PHONY: up
up: ## docker compose up -d --build
	$(COMPOSE) up -d --build

.PHONY: down
down: ## docker compose down (keeps volumes)
	$(COMPOSE) down

.PHONY: restart
restart: ## Restart the bridge container
	$(COMPOSE) restart bridge

.PHONY: logs
logs: ## Tail bridge logs
	$(COMPOSE) logs -f bridge

.PHONY: logs-web
logs-web: ## Tail nginx logs
	$(COMPOSE) logs -f web

.PHONY: ps
ps: ## Show compose service status
	$(COMPOSE) ps

.PHONY: shell
shell: ## Exec a shell in the bridge container
	$(COMPOSE) exec bridge sh

.PHONY: docker-dump
docker-dump: ## Run a one-shot `bridge dump` in a fresh container
	$(COMPOSE) run --rm bridge dump

# ── systemd service (production) ───────────────────────────────────

.PHONY: svc-logs
svc-logs: ## Tail live service logs (journalctl -f)
	journalctl -u $(SERVICE) -f

.PHONY: svc-logs-today
svc-logs-today: ## Show today's service logs
	journalctl -u $(SERVICE) --since today

.PHONY: svc-status
svc-status: ## Show service status + last log lines
	systemctl status $(SERVICE)

.PHONY: svc-start
svc-start: ## Start the service now
	sudo systemctl start $(SERVICE)

.PHONY: svc-stop
svc-stop: ## Stop the service now
	sudo systemctl stop $(SERVICE)

.PHONY: svc-restart
svc-restart: ## Restart the service (use after rebuilding the binary)
	sudo systemctl restart $(SERVICE)

.PHONY: svc-enable
svc-enable: ## Enable + start on boot (and now)
	sudo systemctl enable --now $(SERVICE)

.PHONY: svc-disable
svc-disable: ## Disable on boot + stop now
	sudo systemctl disable --now $(SERVICE)
