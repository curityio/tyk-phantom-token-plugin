PLUGIN_IMG ?= tyk-phantom-token-plugin:dev

.PHONY: build docker-build up down logs bundle

build: ## Build local Go binary (for dev, not used in container)
	CGO_ENABLED=0 go build -trimpath -o ./dist/phantom-plugin ./main.go

docker-build: ## Build multi-arch plugin image via Dockerfile.plugin
	docker buildx build --platform linux/amd64,linux/arm64 \
		-t $(PLUGIN_IMG) -f Dockerfile.plugin . --push=false

up: ## Bring up plugin + bundle-server (expects external network attached in compose)
	docker compose up -d

down:
	docker compose down

logs:
	docker compose logs -f phantom-plugin
