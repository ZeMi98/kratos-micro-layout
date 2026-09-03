# Build and run any command in this repo. Pass the package path, e.g.:
#
#   docker build --build-arg SERVICE=app/user_center/cmd/user_center -t user_center .
#   docker build --build-arg SERVICE=app/gateway/cmd/gateway -t gateway .
#   docker run -p 8000:8000 -v $(pwd)/configs:/data/configs user_center -conf /data/configs/user_center.yaml
#
# The pure-Go sqlite driver keeps CGO_ENABLED=0 viable; switch to a CGO build
# only if you adopt a driver that needs it.
ARG SERVICE=app/user_center/cmd/user_center

FROM golang:1.25 AS builder
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
