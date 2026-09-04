# Build and run any command in this repo. Pass the package path, e.g.:
#
#   docker build --build-arg SERVICE=app/user_center/cmd/user_center -t user_center .
#   docker build --build-arg SERVICE=app/gateway/cmd/gateway -t gateway .
#   docker run -p 8000:8000 -v $(pwd)/configs:/data/configs user_center -conf /data/configs/user_center.yaml
#
# Both supported SQL drivers (go-sql-driver/mysql, jackc/pgx) are pure Go, so
# the static CGO_ENABLED=0 build stays viable. go.work is copied along with the
# source: the nested app/user_center module resolves api/ and pkg/ through it,
# and every third-party dependency is declared in the root go.mod.
ARG SERVICE=app/user_center/cmd/user_center

# Keep this >= the `go` directive in go.mod: an older builder refuses the module
# rather than downloading a newer toolchain.
FROM golang:1.26 AS builder
ARG SERVICE
WORKDIR /src
# Cache module downloads between image builds.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w" -o /out/app ./${SERVICE}

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /out/app /usr/local/bin/app
# user_center listens on 8000 (HTTP) and 9000 (gRPC); the gateway on 8080.
EXPOSE 8000 9000 8080
WORKDIR /data
CMD ["app"]
