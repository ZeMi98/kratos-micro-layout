GOHOSTOS:=$(shell go env GOHOSTOS)
GOPATH:=$(shell go env GOPATH)
VERSION=$(shell git describe --tags --always)

.PHONY: init
# init env
init:
	go install github.com/google/wire/cmd/wire@latest
	go install github.com/bufbuild/buf/cmd/buf@latest

.PHONY: api
# generate api proto (api/** -> go, grpc, http, openapi)
api:
	buf generate --template buf.gen.yaml

.PHONY: config
# generate internal config protos (app/*/internal/conf -> *.pb.go)
config:
	buf generate --template buf.gen.config.yaml
	buf generate --template buf.gen.gw.yaml --path app/gateway/internal/conf/gateway.proto

.PHONY: ent
# regenerate ent ORM code after editing internal/data/ent/schema
ent:
	cd app/user_center/internal/data/ent && go run generate.go

.PHONY: wire
# regenerate dependency-injection code
wire:
	cd app/user_center/cmd/user_center && wire

.PHONY: build
# build all commands into bin/ (root module + the app/user_center nested module)
build:
	mkdir -p bin/ && go build -ldflags "-X main.Version=$(VERSION)" -o ./bin/ ./... ./app/user_center/...

.PHONY: run-user-center
# run the user_center service locally (reads configs/user_center.yaml)
run-user-center:
	go run ./app/user_center/cmd/user_center

.PHONY: run-gateway
# run the gateway locally (reads configs/gateway.yaml; needs Nacos)
run-gateway:
	go run ./app/gateway/cmd/gateway

.PHONY: test
# run all tests (root module + the app/user_center nested module)
test:
	go test ./... ./app/user_center/...

.PHONY: generate
# regenerate ORM and DI code.
# NOTE: no bare `go mod tidy` here. app/user_center is a nested module with a
# minimal go.mod that borrows every dependency from the root go.mod through
# go.work; tidying the root would prune the deps only app/user_center uses.
# Add a new dependency with `go get <mod>` at the repo root instead.
generate:
	cd app/user_center/internal/data/ent && go run generate.go
	cd app/user_center/cmd/user_center && wire

.PHONY: all
# generate all code
all:
	make api
	make config
	make generate

# show help
help:
	@echo ''
	@echo 'Usage:'
	@echo ' make [target]'
	@echo ''
	@echo 'Targets:'
	@awk '/^[a-zA-Z\-\_0-9]+:/ { \
	helpMessage = match(lastLine, /^# (.*)/); \
		if (helpMessage) { \
			helpCommand = substr($$1, 0, index($$1, ":")); \
			helpMessage = substr(lastLine, RSTART + 2, RLENGTH); \
			printf "\033[36m%-22s\033[0m %s\n", helpCommand,helpMessage; \
		} \
	} \
	{ lastLine = $$0 }' $(MAKEFILE_LIST)

.DEFAULT_GOAL := help
