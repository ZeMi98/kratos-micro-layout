# kratos-micro-layout — build & code generation.
#
# `make` alone prints the target list. A target's help text is the `## `
# comment on its own line; extra prose goes in plain `# ` comments.
#
# Three generators feed the tree, and `make all` runs all of them:
#
#   buf   api/**.proto             -> *.pb.go / *_grpc.pb.go / *_http.pb.go
#                                     + pkg/docs/specs/<domain>/openapi.yaml (one per domain)
#   buf   app/*/internal/conf      -> config *.pb.go
#   ent   internal/data/ent/schema -> the ORM (client, queries, migrations)
#   wire  cmd/user_center          -> wire_gen.go (dependency injection)
#
# Never hand-edit a generated file; edit its source and re-run the target.
# Generated files are committed, so a source change and its regeneration
# belong in the same commit.

VERSION := $(shell git describe --tags --always)

.PHONY: init submodule-init submodule-guard api config ent wire generate all build \
	test run-user-center run-gateway middleware-up middleware-down middleware-search-up \
	tidy help

# app/user_center is a git submodule (the standalone service-template repo). A plain
# `git clone` — and `kratos new` — does NOT populate it, which leaves go.work pointing
# at an empty dir so EVERY go command fails with a cryptic "module not found". `init`
# pulls it in; `submodule-guard` (a prerequisite of the go targets below) fails fast
# with a clear message when it is still missing.
init: submodule-init ## init the git submodule + install the codegen CLIs (buf, wire)
	go install github.com/google/wire/cmd/wire@latest
	go install github.com/bufbuild/buf/cmd/buf@latest

submodule-init: ## fetch/initialize the app/user_center git submodule
	@if [ -f .gitmodules ]; then git submodule update --init --recursive; \
	else echo "  no .gitmodules — nothing to init"; fi

submodule-guard:
	@if [ ! -f app/user_center/go.mod ]; then \
		echo "ERROR: app/user_center is empty — it is a git submodule that is not initialized."; \
		echo "       Run 'make init' (or 'git submodule update --init --recursive'), then retry."; \
		exit 1; \
	fi

api: ## regenerate the public API: protos -> Go/gRPC/HTTP stubs + one OpenAPI spec per api/<domain>
	buf generate --template buf.gen.yaml
	@set -e; \
	rm -rf .oapi; \
	for p in $$(find api -mindepth 1 -maxdepth 1 -type d | sort); do \
		name=$$(basename $$p); \
		echo "  openapi -> pkg/docs/specs/$$name/openapi.yaml"; \
		buf generate --template buf.gen.openapi.yaml --path api/$$name; \
		mkdir -p pkg/docs/specs/$$name; \
		mv .oapi/openapi.yaml pkg/docs/specs/$$name/openapi.yaml; \
	done; \
	rm -rf .oapi

# The gateway's config proto sits in its own package (kratos.gateway), so it is
# generated through a second template instead of the shared one.
config: ## regenerate service config types: app/*/internal/conf/*.proto -> *.pb.go
	buf generate --template buf.gen.config.yaml
	buf generate --template buf.gen.gw.yaml --path app/gateway/internal/conf/gateway.proto

ent: submodule-guard ## regenerate the ent ORM after editing internal/data/ent/schema (see docs/ent.md)
	cd app/user_center/internal/data/ent && go run generate.go

wire: submodule-guard ## regenerate wire_gen.go after changing a ProviderSet or constructor signature
	cd app/user_center/cmd/user_center && wire

generate: ent wire ## regenerate ORM + DI code (the usual follow-up to a biz/data change)

all: api config generate ## regenerate everything: protos, configs, ORM and DI

# The nested app/user_center module is invisible to the root module's bare ./...,
# so build and test list it explicitly.
build: submodule-guard ## compile both binaries into bin/
	mkdir -p bin/ && go build -ldflags "-X main.Version=$(VERSION)" -o ./bin/ ./... ./app/user_center/...

test: submodule-guard ## run every test in both modules
	go test ./... ./app/user_center/...

run-user-center: submodule-guard ## run user_center locally (configs/user_center.yaml; needs MySQL)
	go run ./app/user_center/cmd/user_center

run-gateway: submodule-guard ## run the gateway locally (configs/gateway.yaml; needs Nacos)
	go run ./app/gateway/cmd/gateway

middleware-up: ## start the local middleware stack (MySQL + Redis + Nacos) from deploy/middleware/
	docker compose -f deploy/middleware/docker-compose.middleware.yaml up -d

middleware-down: ## stop it again (pass -v by hand to also drop the data volumes)
	docker compose -f deploy/middleware/docker-compose.middleware.yaml down

middleware-search-up: ## also start Elasticsearch + Kibana (the `search` profile)
	docker compose -f deploy/middleware/docker-compose.middleware.yaml --profile search up -d

# app/user_center borrows every dependency from the root go.mod through go.work,
# so a bare `go mod tidy` here would delete the deps only that module uses (ent,
# pgx, mysql, aip...). This target hides go.work and the nested go.mod for the
# duration of the tidy — turning the tree back into a single module — then
# restores both, even if the tidy fails.
tidy: ## prune go.mod/go.sum without breaking the workspace
	@set -e; \
	restore() { \
		[ -f .go.work.bak ] && mv .go.work.bak go.work; \
		[ -f app/user_center/.go.mod.bak ] && mv app/user_center/.go.mod.bak app/user_center/go.mod; \
	}; \
	trap restore EXIT; \
	mv go.work .go.work.bak; \
	mv app/user_center/go.mod app/user_center/.go.mod.bak; \
	go mod tidy

help: ## print this target list
	@echo ''
	@echo 'Usage: make [target]'
	@echo ''
	@awk 'BEGIN { FS = ":.*?## " } \
		/^[a-zA-Z_-]+:.*?## / { printf "\033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

.DEFAULT_GOAL := help
