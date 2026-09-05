# kratos-micro-layout — build & code generation.
#
# `make` alone prints the target list. A target's help text is the `## `
# comment on its own line; extra prose goes in plain `# ` comments.
#
# Three generators feed the tree, and `make all` runs all of them:
#
#   buf   api/**.proto             -> *.pb.go / *_grpc.pb.go / *_http.pb.go
#                                     + pkg/docs/specs/<domain>/openapi.yaml (one per domain)
#   buf   app/*/internal/conf/v1 -> config *.pb.go
#   ent   internal/data/ent/schema -> the ORM (client, queries, migrations)
#   wire  cmd/user_center          -> wire_gen.go (dependency injection)
#
# Never hand-edit a generated file; edit its source and re-run the target.
# Generated files are committed, so a source change and its regeneration
# belong in the same commit.
#
# Cross-platform: every recipe is a single portable command (buf/go/git/docker)
# or uses the $(MKDIR)/$(MOVE)/$(RM_RF) helpers below, so the same Makefile
# runs under a POSIX sh (macOS/Linux) and under cmd.exe (Windows). On Windows
# use a native GNU make (scoop/choco `make` or `mingw32-make`); recipes then
# run through cmd.exe, which this file forces explicitly so behaviour does not
# depend on which make distribution is installed. WSL behaves like Linux.
# No recipe relies on find/grep/sed/xargs/trap or shell loops — iteration is
# done by make itself ($(wildcard) + per-item sub-targets).

VERSION := $(shell git describe --tags --always)
ifeq ($(VERSION),)
VERSION := dev
endif

ifeq ($(OS),Windows_NT)
SHELL := cmd.exe
MKDIR = if not exist "$(1)" mkdir "$(1)"
MOVE = move /y
RM_RF = if exist "$(1)" rmdir /s /q "$(1)"
HELP_CMD = findstr /c:": \#\# " $(MAKEFILE_LIST)
else
MKDIR = mkdir -p "$(1)"
MOVE = mv -f
RM_RF = rm -rf "$(1)"
HELP_CMD = awk 'BEGIN { FS = ":.*?\#\# " } /^[a-zA-Z_-]+:.*?\#\# / { printf "\033[36m%-18s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
endif

# The codegen targets stage files through shared paths (.oapi, *.bak), so they
# must not interleave: never run make with -j, and never combine `tidy` with
# another target on one command line (its target-scoped GOWORK=off would leak
# into the others).
.NOTPARALLEL:
.DELETE_ON_ERROR:

# Domains and services are discovered by make at parse time, so an incubated
# service or a new api/<domain> needs no registration here. (notdir runs last:
# on a trailing-slash path it yields "" — order matters.)
API_DOMAINS := $(notdir $(patsubst %/,%,$(wildcard api/*/)))
CONFIG_SERVICES := $(patsubst app/%/internal/conf,%,$(wildcard app/*/internal/conf))

.PHONY: init api api-ext api-domain api-clean config ent wire generate all build \
	test run-user-center run-gateway middleware-up middleware-down middleware-search-up \
	tidy help \
	$(API_DOMAINS:%=api-%) $(CONFIG_SERVICES:%=config-%)

# app/user_center is the example service and a git submodule (the standalone
# service-template repo kratos-micro-sub-service-layout). Neither `git clone` nor
# `kratos new` fetches submodules, so populate it yourself once after cloning — either
# `git submodule update --init --recursive`, or scaffold it the way you'd add any
# service: `kratos new app/user_center --nomod -r https://github.com/ZeMi98/kratos-micro-sub-service-layout.git`.
# The Makefile deliberately does NOT manage this; `init` only installs the codegen CLIs.
init: ## install the codegen CLIs (buf, wire)
	go install github.com/google/wire/cmd/wire@latest
	go install github.com/bufbuild/buf/cmd/buf@latest

api: api-ext $(API_DOMAINS:%=api-%) api-clean ## regenerate the shared pkg/validate extension, then the public API per domain: protos -> Go/gRPC/HTTP stubs + that domain's OpenAPI spec

# The shared validation extension (pkg/validate/v1) declares no service, so it
# is generated on its own: a Go stub only, no per-domain stubs or spec.
api-ext:
	@echo   pkg/validate/v1 - shared validate.v1.error_message extension
	buf generate --template buf.gen.ext.yaml

# One sub-make per domain keeps every recipe line a single portable command.
# Static pattern rules (not bare `api-%:`) because GNU make 3.81 — the default
# on macOS — does not apply pattern rules to .PHONY targets.
$(API_DOMAINS:%=api-%): api-%:
	@$(MAKE) --no-print-directory api-domain DOMAIN=$*

api-domain:
	@echo   api/$(DOMAIN) - stubs + pkg/docs/specs/$(DOMAIN)/openapi.yaml
	buf generate --template buf.gen.yaml --path api/$(DOMAIN)
	$(call MKDIR,pkg/docs/specs/$(DOMAIN))
	$(MOVE) .oapi/openapi.yaml pkg/docs/specs/$(DOMAIN)/openapi.yaml

api-clean:
	$(call RM_RF,.oapi)

# Each service's config proto lives in its own package (mirroring its
# directory under the app/ buf module). The wildcard above discovers every
# service, so an incubated one needs no Makefile or template registration.
config: $(CONFIG_SERVICES:%=config-%) ## regenerate service config types: app/*/internal/conf/v1/*.proto -> *.pb.go

$(CONFIG_SERVICES:%=config-%): config-%:
	@echo   conf - app/$*/internal/conf/v1
	buf generate --template buf.gen.conf.yaml --path app/$*/internal/conf

ent: ## regenerate the ent ORM after editing internal/data/ent/schema (see docs/ent.md)
	cd app/user_center/internal/data/ent && go run generate.go

wire: ## regenerate wire_gen.go after changing a ProviderSet or constructor signature
	cd app/user_center/cmd/user_center && wire

generate: ent wire ## regenerate ORM + DI code (the usual follow-up to a biz/data change)

all: api config generate ## regenerate everything: protos, configs, ORM and DI

# The nested app/user_center module is invisible to the root module's bare ./...,
# so build and test list it explicitly.
build: ## compile both binaries into bin/
	$(call MKDIR,bin)
	go build -ldflags "-X main.Version=$(VERSION)" -o ./bin/ ./... ./app/user_center/...

test: ## run every test in both modules
	go test ./... ./app/user_center/...

run-user-center: ## run user_center locally (configs/user_center.yaml; needs MySQL)
	go run ./app/user_center/cmd/user_center

run-gateway: ## run the gateway locally (configs/gateway.yaml; needs Nacos)
	go run ./app/gateway/cmd/gateway

middleware-up: ## start the local middleware stack (MySQL + Redis + Nacos) from deploy/middleware/
	docker compose -f deploy/middleware/docker-compose.middleware.yaml up -d

middleware-down: ## stop it again (pass -v by hand to also drop the data volumes)
	docker compose -f deploy/middleware/docker-compose.middleware.yaml down

middleware-search-up: ## also start Elasticsearch + Kibana (the `search` profile)
	docker compose -f deploy/middleware/docker-compose.middleware.yaml --profile search up -d

# app/user_center borrows every dependency from the root go.mod through go.work,
# so a bare `go mod tidy` here would delete the deps only that module uses (ent,
# pgx, mysql...). This target hides go.work and the nested go.mod for the
# duration of the tidy — turning the tree back into a single module — then
# restores both. A failed tidy restores them as well and still fails the target
# (the `||` branch ends in `exit 1`, which both sh and cmd.exe honour), so a
# broken go.mod cannot slip through as a green run.
tidy: export GOWORK = off
tidy: ## prune go.mod/go.sum without breaking the workspace
	$(MOVE) go.work .go.work.bak
	$(MOVE) app/user_center/go.mod app/user_center/.go.mod.bak
	go mod tidy || ( $(MOVE) .go.work.bak go.work && $(MOVE) app/user_center/.go.mod.bak app/user_center/go.mod && exit 1 )
	$(MOVE) .go.work.bak go.work
	$(MOVE) app/user_center/.go.mod.bak app/user_center/go.mod

help: ## print this target list
	@echo Usage: make [target]
	@$(HELP_CMD)

.DEFAULT_GOAL := help
