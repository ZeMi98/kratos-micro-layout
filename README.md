# kratos-micro-layout

基于 [Kratos v3](https://go-kratos.dev) 的微服务 monorepo 模板：一个 HTTP 网关 + 一个用户中心服务模板 + 共享基础库（日志、Nacos、传输中间件套件、JWT 令牌引擎、ID 生成、雪花算法、API 文档）。

存储层只保留 **ent**（schema 用 Go 写，客户端代码生成），默认 **MySQL**、两行配置即可切到 PostgreSQL（见 [docs/ent.md](docs/ent.md)）；日志统一用标准库 **log/slog**（Text/JSON + lumberjack 轮转）；服务注册 / 发现 / 配置中心统一由 **Nacos** 承担（kratos 生态中最主流、也是唯一同时提供注册与热更新配置的后端）；网关内建**限流 + 熔断**；HTTP 响应统一为 `code` / `message` / `data` **三字段信封**；API 文档由 buf 生成、`go:embed` 内嵌进二进制，服务自带 `/swagger`；分布式 ID 由 `pkg/snowflake` 就地生成。全部配置集中在根目录 `configs/`，本地中间件在 `deploy/`。

## 架构

```
                        ┌─────────────┐
   client ──HTTP──►     │   gateway   │  :8080  反向代理 + CORS + 负载均衡
                        │             │         + 边缘限流 + 每后端熔断
                        └──────┬──────┘
                               │ 服务发现 (user_center.http)
                               │ Nacos
                               ▼
                        ┌─────────────┐
                        │ user_center │  :8000 HTTP / :9000 gRPC
                        │             │  限流(BBR/token) + 鉴权 + 校验 + /swagger
                        └──────┬──────┘
                               │
                 ┌─────────────┴─────────────┐
                 │     biz ─► data (ent)     │  mysql(默认)/postgres
                 └───────────────────────────┘
```

这是**双层模板**：

| 层 | 仓库 | 用途 |
|---|---|---|
| 项目模板 | 本仓库整体 | `kratos new PROJECT -r https://github.com/ZeMi98/kratos-micro-layout.git` 创建新项目 |
| 服务模板 | `app/user_center/` | `kratos new app/SERVICE --nomod -r https://github.com/ZeMi98/kratos-micro-sub-service-layout.git` 在项目内创建新微服务 |

> **两个模板仓库**：项目模板 [github.com/ZeMi98/kratos-micro-layout](https://github.com/ZeMi98/kratos-micro-layout)（本仓库整体）· 服务模板 [github.com/ZeMi98/kratos-micro-sub-service-layout](https://github.com/ZeMi98/kratos-micro-sub-service-layout)（即 `app/user_center/`）。
> `kratos new` 用 `-r/--repo` 指定模板仓库、`--nomod` 复用项目根模块；别误用 `-b/--branch`（那是指定分支，写成 `-b <url>` 会报 `Remote branch ... not found`）。

## 目录结构

```
api/user/v1/                 服务对外的 proto 契约（DTO）与生成代码
app/user_center/             服务模板：用户中心（登录注册/鉴权/改密）
  cmd/user_center/           入口 + Wire 注入
  internal/conf/v1/          配置 proto（make config 生成）
  internal/server/           HTTP/gRPC server 装配 + 鉴权 selector + 文档路由挂载
  internal/service/          DTO ↔ DO 转换层
  internal/biz/              领域模型、用例、Repo 接口、错误；JWT/ID 引擎的薄 conf provider
  internal/data/             薄入口：ProviderSet 指向 ent 子包
    ent/                     ent schema + 生成代码 + repo（自包含，唯一存储实现）
app/gateway/                 网关（无 biz/data，手工装配）
  internal/proxy/            服务发现 + selector 负载均衡 + 反向代理 + 每后端熔断
  internal/server/           监听器、CORS、边缘限流 filter、路由注册
pkg/docs/                    API 文档：make api 按 domain 生成的 specs/<domain>/openapi.yaml（go:embed）+ /swagger UI
pkg/log/                     日志构建：标准库 log/slog（Text/JSON）+ lumberjack 文件轮转
pkg/middleware/              可复用传输中间件：鉴权、编解码(信封+protojson)、日志、校验、限流
pkg/jwt/                     HS256 令牌引擎（Manager/Claims）：签发与校验 access/refresh token
pkg/idgen/                   ID 抽象：Generator 接口 + 雪花实现（int64 主键）
pkg/nacos/                   Nacos 注册/发现、配置中心 Source 封装
pkg/snowflake/               Twitter Snowflake 分布式 ID 生成器（64-bit，无协调、可排序）
pkg/validate/v1/             共享 proto 扩展：(validate.v1.error_message) 自定义校验失败描述
configs/user_center.yaml     每服务一份配置，集中存放
configs/gateway.yaml
deploy/                      部署物料：middleware/（中间件 compose + .env.example）、business/（业务服务 compose/k8s 模板）、script/（迁移 SQL）
docs/                        专题文档：ent.md（存储层全流程）
Makefile / buf*.yaml         代码生成入口（`make` 或 `make help` 列出全部目标）
Dockerfile                   参数化构建任意服务的镜像
```

## 多模块与 go.work 工作区

本仓库是一个 **Go workspace（多模块）**：根模块承载 `api/`、`pkg/` 与 `app/gateway/`，而 `app/user_center/` 是一个**嵌套的独立模块**（自带 `go.mod`，以 **git 子模块**形式挂进本仓库，同时作为服务模板托管在 [kratos-micro-sub-service-layout](https://github.com/ZeMi98/kratos-micro-sub-service-layout)）。根目录的 `go.work`（**已提交**）把两者纳入同一工作区：

```
go 1.26.0

use (
	.                 # 根模块：api/、pkg/、app/gateway/
	./app/user_center # 嵌套的服务模块
)
```

- **为什么 user_center 必须有 `go.mod`**：`kratos new --nomod -r <repo>` 生成新服务时，CLI 会读取模板 `go.mod` 的 module path 作为替换基准；缺了它直接报错。所以 `app/user_center/go.mod` 不能删。
- **为什么它是最小的**：`app/user_center/go.mod` 只有 `module` + `go` 两行，不列任何 `require`。它对第三方依赖（kratos、ent、mysql、pgx…）以及对根模块 `api/`、`pkg/` 的引用，全部经 `go.work` 从**根模块的构建列表**解析 —— 服务模板因此不必重复维护一份依赖清单。
- **对命令行的影响**：嵌套模块会被父模块的裸 `./...` 排除，故 `Makefile` 的 `build`/`test` 显式带上 `./app/user_center/...`；工作区内 `cd app/user_center && go build ./...` 与根目录 `go build ./app/user_center/...` 均可正常解析。
- **加依赖只在根目录做**：根模块自身并不 import ent/mysql/pgx（只有 user_center 用），因此**不要在根目录裸跑 `go mod tidy`** —— 它会把"仅 user_center 使用"的依赖当作无用而剪掉，破坏工作区解析。新增依赖统一用 `go get <module>` 加到根 `go.mod`；确实要清理时用 `make tidy`（临时隐藏 `go.work` 与嵌套 `go.mod`、把整棵树还原成单模块后再 tidy，退出时必定恢复）。`make generate` 刻意不含 tidy。
- `go.work.sum` 是工作区派生的校验文件，已在 `.gitignore` 忽略；`go.work` 本身必须提交，否则新克隆无法解析 user_center。
- **`app/user_center` 是 git 子模块**：`git clone` / `kratos new` 不会自动拉取，未初始化时该目录为空、`go.work` 解析失败。克隆后自行拉取一次：`git clone --recurse-submodules`、或 `git submodule update --init --recursive`、或按新增服务的方式 `kratos new app/user_center --nomod -r https://github.com/ZeMi98/kratos-micro-sub-service-layout.git`。`Makefile` 不再管理子模块（`make init` 只装 buf/wire CLI）。
- **`go:embed` 不能跨模块**：这也是 OpenAPI 文档落在根模块的 `pkg/docs/` 而不是 `app/user_center/` 里的原因（见「API 文档」）。

## 快速开始

### 1. 用模板创建新项目

> **重要：`app/user_center` 是 git 子模块**（独立的服务模板仓库
> [kratos-micro-sub-service-layout](https://github.com/ZeMi98/kratos-micro-sub-service-layout)）。
> `git clone` 与 `kratos new` **都不会**自动拉取子模块；缺了它，`go.work` 会指向一个空目录，
> 于是所有 `go build` / `make` 都以费解的“找不到模块”失败。克隆后务必初始化子模块 ——
> 克隆时带 `--recurse-submodules`，或事后手动 `git submodule update --init --recursive`（`Makefile` 不再代管这一步）。

```bash
# 安装 Kratos CLI（若未安装）
go install github.com/go-kratos/kratos/cmd/kratos/v3@latest

# 方式一：直接克隆（推荐带 --recurse-submodules，一步到位）
git clone --recurse-submodules https://github.com/ZeMi98/kratos-micro-layout.git my-project
cd my-project

# 方式二：基于本模板创建项目（替换 REPO 为你的 fork 地址）
kratos new my-project -r https://github.com/ZeMi98/kratos-micro-layout.git
cd my-project

# 若上一步没带 --recurse-submodules，先手动拉取子模块
git submodule update --init --recursive

# 安装 buf、wire CLI（代码生成工具）
make init
```

### 2. 在项目内新增微服务

`app/user_center` 就是服务模板。把它推到独立仓库后，可在任意项目内孵化新服务：

```bash
# 服务模板托管在独立仓库 kratos-micro-sub-service-layout，直接用它孵化新服务：
# （--nomod：不在新服务里生成独立 go.mod，复用项目根模块）
kratos new app/order --nomod -r https://github.com/ZeMi98/kratos-micro-sub-service-layout.git

# 想用自己的模板？fork kratos-micro-sub-service-layout，把上面的 -r 换成你的 fork 地址
```

新建的 `app/order` 与 `user_center` 同构：同样的 internal 分层、同样的配置加载逻辑。
创建后记得：

1. 全局替换模块路径中的 `user_center` → `order`（`go mod edit -module` 已由 `--nomod` 处理，主要是 import 路径）
2. 重命名 `configs/user_center.yaml` → `configs/order.yaml`，并同步 `cmd/*/main.go` 里的 `nacosConfigDataID` 与 `-conf` 默认值
3. 无需登记 buf：新服务的 conf proto 自动落入 `buf.yaml` 的 `app` 模块，`make config` 按 `app/*/internal/conf` 目录循环自动发现并生成
4. 改 `internal/data/ent/generate.go` 里的 `Package`（ent 生成代码的 import path），详见 [docs/ent.md](docs/ent.md)

### 3. 起本地中间件

存储层只接了 MySQL / PostgreSQL 驱动，本地开发用 `deploy/` 里的 compose 一键拉起：

```bash
make middleware-up     # MySQL :3306 + Redis :6379 + Nacos :8848/:9848
make middleware-down   # 停掉（保留数据卷）
```

- 容器的端口、账号密码与 `configs/user_center.yaml` 的默认 DSN 完全对齐，起来即可用，不必再改配置。
- compose 里的 `MYSQL_DATABASE: user_center` 会**建库**；ent 的 `auto_migrate` 只**建表**，不会建库。用自己的 MySQL 时先手动 `CREATE DATABASE`。
- `redis` 目前是**预留**的：配置里有 `data.redis` 块，但还没有代码读它。只需要数据库的话，删掉 compose 里的 `redis` / `nacos` 两个 service 即可。
- 端口被占用（本机已有 MySQL，或用 SSH 隧道转发了一台远端的）时 `up` 会报 `address already in use`：腾出端口，或只改映射的宿主侧（`"13306:3306"`）再用 `KRATOS_DB_SOURCE` 指过去。
- 这份 compose 是**本地开发用**的，不是生产清单（无 TLS、无备份、无资源限制）。

### 4. 本地运行（无需 Nacos）

模板默认 `registry.address` 为空 —— Nacos 整链路关闭，开箱即跑：

```bash
make run-user-center   # auto_migrate=true，启动时 ent 自动建表；监听 :8000/:9000

# 冒烟
curl -X POST localhost:8000/v1/auth/register -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"secret123","email":"alice@example.com"}'
curl -X POST localhost:8000/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"secret123"}'

open http://localhost:8000/swagger      # 内嵌 Swagger UI；spec 在 /openapi.yaml
```

### 5. 完整模式（带注册中心 + 网关）

```bash
make middleware-up     # Nacos 同时承担注册中心与配置中心
#   configs/*.yaml:
#     registry: { address: 127.0.0.1:8848 }

make run-user-center   # 服务注册为 user_center.http / user_center.grpc
make run-gateway       # 网关监听 :8080，经 Nacos 发现后端

curl -X POST localhost:8080/v1/auth/login -H 'Content-Type: application/json' \
  -H 'Origin: http://localhost:3000' -d '{"username":"alice","password":"secret123"}'
#   经网关 + CORS；拿响应里的 access_token 再调 GET /v1/users/profile（Bearer 头）
```

> `registry.address` 非空时，各服务同时拉取 Nacos 配置中心的同名 dataID（`<service>.yaml`）并热更新；留空时本地 yaml 即唯一配置来源。
> Nacos 必须同时暴露 `8848`（HTTP）与 `9848`（gRPC）—— Go SDK 走 9848，只发布 8848 会让客户端一直卡住。

## Proto 管理与 buf 缓存

proto 依赖（`googleapis` 等）通过 [buf](https://buf.build) 管理，**没有 `third_party/` 目录**：

- `buf.yaml` 声明 workspace 模块（`api`、`app`）与 BSR 依赖；proto package 与相对模块根的目录严格一致（`user.v1`、`<service>.internal.conf.v1`），`buf lint` 跑默认规则、零豁免
- `buf.lock` 锁定依赖版本
- 依赖的 proto 只会被拉取到**本地缓存目录**，构建时从缓存读取：
  - Linux / macOS：`~/.cache/buf/v3`
  - 可用 `BUF_CACHE_DIR` 环境变量重定向；删除该目录即强制重新拉取

```bash
make init          # 安装 buf 与 wire CLI
buf dep update     # 按 buf.yaml 拉取/更新依赖（写入 buf.lock 与缓存）

make api           # pkg/validate/v1 扩展 + api/**.proto → go / grpc / http 桩 + pkg/docs/specs/<domain>/openapi.yaml（每 domain 一份）
make config        # 各服务 internal/conf/v1 → *.pb.go
```

生成模板分三个文件：`buf.gen.ext.yaml`（`pkg/` 里的共享 proto —— 目前只有 `validate.v1.error_message` 扩展，只出 Go 桩）、`buf.gen.yaml`（api 公共契约：go/grpc/http 桩 + 按 domain 各一份的 OpenAPI）、`buf.gen.conf.yaml`（各服务配置，`make config` 按目录循环逐个 `--path` 生成）。
`buf.yaml` 相应声明了三个模块：`api`、`app`、`pkg`。跨模块 import 是合法的（`api/user/v1/auth.proto` 就 import 了 `validate/v1/validate.proto`），所以 api 里的字段能直接用 `pkg` 模块声明的扩展注解。
每个服务的配置 proto 通过各自的 `--path` 单独生成；它们的 package 带服务名前缀（`user_center.internal.conf.v1`、`gateway.internal.conf.v1`），避免同名消息（Bootstrap、Server…）在同一次 workspace 编译中冲突。

> `buf.gen.yaml` 里 OpenAPI 插件的 `opt` 值**不能含逗号** —— buf 会先按逗号拆分再传给插件，多出来的部分被当成未知 flag（`no such flag -message`）。

## 配置说明

每个服务一份 `configs/<service>.yaml`，集中存放、以服务名命名。加载顺序（见各 `cmd/*/main.go` 的 `loadConfig`）：

1. 本地文件 `configs/<service>.yaml`（永远生效，兜底）
2. 当 `registry.address` 非空：Nacos 配置中心同名 dataID（`<service>.yaml`）合并覆盖，支持热更新
3. `${VAR:default}` 占位符：kratos 默认 resolver 在合并后解析，从环境变量取值

> **环境变量注入的正确姿势**：kratos 的 env source 只剥离 `KRATOS_` 前缀（及一个前导下划线），**不会**把键小写化、也**不会**把 `_` 映射成 `.`。因此 `KRATOS_AUTH_JWT_SECRET` 只会变成顶层键 `AUTH_JWT_SECRET`，无法直接覆盖嵌套的 `auth.jwt_secret`。要让某个值走环境变量，用占位符引用它 —— `jwt_secret: ${AUTH_JWT_SECRET:change-me-in-production}`，再设 `KRATOS_AUTH_JWT_SECRET=...`（前缀被剥离后正好命中占位符查找的键）。模板里的 `auth.jwt_secret` 与 `data.database.source` 就是这么做的。

> Duration 一律使用 protojson 秒格式（如 `7200s`），不能写 `2h`。

关键项速览（`configs/user_center.yaml`）：

| 键 | 说明 |
|---|---|
| `data.database.driver` | `mysql`（默认）或 `postgres`，详见「数据库接入」 |
| `data.database.source` | DSN，用 `${DB_SOURCE:...}` 占位符注入凭据；MySQL 别省 `parseTime=true&loc=Local` |
| `data.database.debug` | 打印每条 SQL，仅开发环境开 |
| `data.database.auto_migrate` | 启动时 `Schema.Create` 建表；**生产关掉**，改用版本化迁移（[docs/ent.md](docs/ent.md)） |
| `auth.jwt_secret` / `*_ttl` | JWT 密钥（用 `${AUTH_JWT_SECRET:...}` 注入）与 access/refresh token 有效期 |
| `snowflake.node_id` | 本实例的雪花节点号，集群内必须唯一（`[0,1023]`） |
| `log.level` / `output` / `format` | 日志级别、输出目标（stdout/stderr/file）与编码（text/json）；引擎固定为标准库 log/slog |
| `registry.address` | Nacos 服务器地址；留空则禁用注册/发现/远程配置（本地零依赖默认） |
| `registry.namespace_id` / `group` | Nacos 命名空间与分组，留空即用 public / DEFAULT_GROUP |
| `middleware.ratelimit` | 服务端限流：`enabled` / `type`(bbr\|token) / `qps` / `burst` |

网关（`configs/gateway.yaml`）另有 `gateway.routes`、`gateway.cors` 与 `middleware.circuit_breaker`（每后端熔断），详见「Gateway 网关」与「限流与熔断」。

## HTTP API 约定

user_center 的 HTTP/gRPC server 在 `internal/server/` 装配横切能力，中间件本体统一放在 `pkg/middleware/`（新服务直接复用，无需重写）。中间件链（外→内）：`recovery → tracing → logging → ratelimit → auth → validate`。限流置于 logging 之后（被限流的 429 仍留痕）、auth 之前（流量洪峰时不浪费 JWT 验签 CPU）；默认关闭，开启方式见「限流与熔断」。

### 中间件套件与接入

横切能力全部沉淀在 `pkg/middleware/`，新服务直接复用；`internal/server/` 只按配置把它们**装配成链**：

| 文件 | 主要导出 | 职责 |
|---|---|---|
| `codec.go` | `RequestDecoder` / `ResponseEncoder` / `ErrorEncoder` / `Envelope` | protojson 编解码 + 统一响应信封（仅 HTTP 挂载） |
| `logging.go` | `RequestLogger(logger)` | 每请求一行结构化日志，**不打印请求体** |
| `ratelimit.go` | `RateLimitServer(enabled, kind, qps, burst)` / `RateLimitFilter(...)` / `NewTokenLimiter(...)` | 服务端限流中间件（未启用返回 `nil`）/ 网关 HTTP filter |
| `auth.go` | `TokenVerifier` 接口 + `TokenAuth(verifier, unauthorizedErr)`；`UserIDFromContext` / `ClaimsFromContext` | 抽取 bearer、校验、把 `AuthClaims` 注入 `context` |
| `validate.go` | `Validator()` / `ProtoValidator` / `ValidationFailedReason` | protovalidate 校验中间件：失败文案取 proto 里声明的 `(validate.v1.error_message)` |

链在 `internal/server/http.go`（gRPC 同构，见 `grpc.go`）按**外→内**组装。限流是可选的 —— `RateLimitServer` 未启用时返回 `nil`，所以先建基础切片、非 `nil` 才追加，其余顺序不受影响：

```go
mws := []middleware.Middleware{
	recovery.Recovery(),          // 最外层：兜住下游 panic
	tracing.Server(),             // 开 span，日志/handler 继承 trace_id
	pkgmw.RequestLogger(logger),
}
rl := mw.GetRatelimit()
if limiter := pkgmw.RateLimitServer(rl.GetEnabled(), rl.GetType(), int(rl.GetQps()), int(rl.GetBurst())); limiter != nil {
	mws = append(mws, limiter)    // 默认关闭时不进链
}
mws = append(mws,
	authMiddleware(authUC), // 选择性鉴权（selector 白名单）
	pkgmw.Validator(),      // 最内层：贴近 handler 校验入参
)
```

HTTP 额外用 `http.RequestDecoder/ResponseEncoder/ErrorEncoder` 挂 protojson 与信封；gRPC 保留原生 status code、不套信封，其余中间件与 HTTP 完全一致。

**接入一个新中间件**：把可复用逻辑写进 `pkg/middleware/<name>.go`（导出一个返回 `middleware.Middleware` 的构造器），再在 `http.go` 和 `grpc.go` 的 `mws` 切片里按期望顺序 `append` —— 两个 transport 要同步改以免行为漂移。服务专属、不适合下沉的部分（把 `biz.AuthUsecase` 适配成 `TokenVerifier`、`selector` 白名单）留在 `internal/server/auth.go`。

### 统一响应信封

所有 HTTP 响应（成功与失败）都包成同一结构 —— **只有三个字段**，**HTTP 状态码恒为 `200`**，业务结果看 `code`：

```json
{ "code": 0, "message": "", "data": { } }
```

| 字段 | 成功 | 失败 |
|---|---|---|
| `code` | `0` | kratos/gRPC 状态码（`400`/`401`/`404`/`409`/`429`/`500`…） |
| `message` | 空字符串 | 错误描述（人类可读，可直接展示给终端用户） |
| `data` | 资源对象或列表包装 | `null` |

失败示例（用户名已存在）：

```json
{ "code": 409, "message": "user already exists", "data": null }
```

三处手写 JSON 的地方保持同一形状，客户端只需一套解析逻辑：

| 出处 | 场景 | HTTP 状态码 |
|---|---|---|
| `pkg/middleware/codec.go` | 服务内所有 proto handler 的成功 / 失败响应 | 恒为 `200`，结果看 `code` |
| `pkg/middleware/ratelimit.go` | 网关边缘限流命中（filter 形态，未进 kratos 链） | 真实 `429` |
| `app/gateway/internal/proxy/handler.go` | 网关自身错误：无健康实例 / 熔断开路 `503`、后端不可达 `502` | 真实状态码 |

> **为什么只有三个字段**：kratos 原生的 `reason`（错误枚举字符串）与 `code` 高度重复，`metadata` 只在需要回传结构化细节时才有用 —— 而 protovalidate 的失败信息已经拼进 `message`。字段越少，前后端约定越不容易漂移。
> 需要机器可读的错误分类时，`reason` 仍然保留在**服务端**：`biz` 层用 `errors.NotFound(v1.ErrorReason_ERROR_REASON_USER_NOT_FOUND.String(), "user not found")` 构造错误，`RequestLogger` 会把 reason 打进日志，gRPC 侧也照常透传；只是不再出现在 HTTP body 里。要恢复给前端，给 `Envelope` 加回一个字段即可。

实现在 `pkg/middleware/codec.go`（`ResponseEncoder`/`ErrorEncoder`）。想恢复语义化 HTTP 状态码，改 `writeEnvelope` 里的 `WriteHeader` 一行即可。**gRPC 不套信封**，沿用原生 status code。

### protojson 序列化

请求体与响应体都用 [protojson](https://protobuf.dev/reference/go/faq#json) 编解码（覆盖 kratos v3 默认的 `encoding/json`），与 gRPC-Gateway / Google API 生态一致：

- 时间戳是 RFC 3339 字符串（`"2026-09-02T08:46:40Z"`），而非 `{"seconds":…,"nanos":…}`
- 枚举用名字（`"USER_STATUS_ACTIVE"`），而非数字
- 字段名 snake_case（`UseProtoNames`），与 `.proto`、`openapi.yaml` 对齐
- int64 编码为字符串，避免 JS 精度丢失
- 零值字段省略（`EmitUnpopulated: false`），响应更紧凑

请求侧同样走 protojson（`pkg/middleware` 的 `RequestDecoder`），因此客户端可把响应里的字段原样回传（枚举名、RFC 3339 时间都能被解析）；未知字段容忍（`DiscardUnknown`），新老版本客户端不会互相打挂。

### 参数校验（protovalidate）

校验规则用 [protovalidate](https://buf.build/docs/protovalidate) 直接写在 `.proto` 上（`buf.validate` 注解），运行时由 `pkgmw.Validator()` 中间件执行 —— **无需代码生成**，改完 proto 跑 `make api` 就生效，HTTP 与 gRPC 共用同一套规则不会漂移。

**规则写在哪**：字段的 `[(buf.validate.field) = {...}]` 注解，用 protovalidate 的**声明式标准规则**（`required`、`string.min_len`、`string.email`…），可读性最好、也是官方推荐的默认写法。**失败文案另起一条注解声明** —— 本仓库自带的 `(validate.v1.error_message)` 扩展（定义在 `pkg/validate/v1/validate.proto`），与规则并列、互不干扰。`api/user/v1/auth.proto` 的实际写法：

```proto
import "validate/v1/validate.proto";   // 共享扩展，只需 import，无需改生成配置

message RegisterRequest {
  string username = 1 [
    (google.api.field_behavior) = REQUIRED,      // 进 OpenAPI 文档的 required 标记
    (buf.validate.field) = {                     // 运行时执行的规则（声明式）
      required: true
      string: {min_len: 3, max_len: 64}
    },
    (validate.v1.error_message) = "username is required and must be between 3 and 64 characters"
  ];                                             // ↑ 客户端被拒时看到的文案

  // 可选字段只约束形状；PATCH 复用的字段另加 ignore: IGNORE_IF_ZERO_VALUE（见 user.proto）
  string nickname = 4 [
    (buf.validate.field) = {string: {max_len: 64}},
    (validate.v1.error_message) = "nickname must be at most 64 characters"
  ];
}
```

**为什么需要这个扩展**：标准规则**自身没有 message 挂点**（protovalidate 里唯一原生的自定义文案只在 CEL 规则的 `Rule.message` 上），而且 `required` 失败会**短路**成一句通用的 `"value is required"`。不挂扩展的话，客户端只能读到 protovalidate 的英文模板（`"must be at least 3 characters"`、`"must be a valid email address"`）。把文案声明在规则旁边，既保留了标准规则的声明式写法，又能让客户端直接看到"该改什么"。

`field_behavior` 与 `buf.validate` 是两件事：前者是**文档/契约**元数据（生成的 OpenAPI 会标 required），后者才是**运行时执行**的规则。只写 `field_behavior` 不会有任何校验效果。

**谁来执行**：`pkg/middleware/validate.go` 的 `ProtoValidator` 调 `protovalidate.Validate(msg)`，对每条违规先通过反射取回该字段的 `(validate.v1.error_message)`（violation 自带 `FieldDescriptor`，`proto.GetExtension` 读回选项值），取不到才回落到规则自身的文案（CEL 规则保留自己的 `message`）；渲染成 `字段路径: 描述` 后用 `; ` 拼成一行，包成 `errors.BadRequest(ValidationFailedReason, …)`。`Validator()` 把它原样返回，挂成中间件：

```go
pkgmw.Validator()
```

**这里刻意不用 kratos 的 `validate.Validator(...)`**：它会用 `err.Error()` 重新包一层 `errors.BadRequest("VALIDATOR", …)`，而 kratos error 的 `Error()` 是 `error: code = … reason = … message = … metadata = …` 的完整格式串 —— 你在 proto 里斟酌好的文案会被埋进那串噪声里，`VALIDATION_FAILED` 这个 reason 也永远浮不上来。自有中间件少一层包装，信封里的 `message` 就是纯文案。

放在中间件链**最内层**（紧贴 handler）的意义是：`recovery` 兜住校验器自身的 panic、`logging` 记下这次 400、`ratelimit` 先把洪峰削掉、`auth` 先确认调用者身份 —— 无效 token 的请求连校验都不会跑，返回 401 而不是 400。规则按消息类型编译一次并缓存，后续请求开销很小。

**校验失败时客户端看到什么**：一次请求会**收集全部**违规字段（不是遇到第一个就返回），HTTP 侧 `code=400`，`message` 是逐字段的自定义描述拼接：

```bash
curl -X POST localhost:8000/v1/auth/register -H 'Content-Type: application/json' \
  -d '{"username":"a","password":"short","email":"not-an-email"}'
```

```json
{
  "code": 400,
  "message": "username: username is required and must be between 3 and 64 characters; email: email is required and must be a valid address, e.g. alice@example.com; password: password is required and must be between 8 and 72 characters",
  "data": null
}
```

前端可以按 `; ` split 后一次性标红所有输入框。gRPC 侧同一份规则返回 `InvalidArgument`，message 完全一致。

**常用标准规则速查**（都写在 `(buf.validate.field) = {...}` 里，与 `(validate.v1.error_message)` 并列）：

| 场景 | 写法 |
|---|---|
| 必填 | `required: true`（与长度规则可叠加，不会重复报错，见下） |
| 字符串长度 | `string: {min_len: 3, max_len: 64}` |
| 邮箱 | `string: {email: true}` |
| 正则 | `string: {pattern: "^[a-z]+$"}`（RE2，不支持反向引用） |
| 枚举 | `enum: {defined_only: true}` |
| 数值范围 | `int32: {gte: 0, lte: 100}` |
| repeated 非空 | `repeated: {min_items: 1}` |
| 时间戳 | `timestamp: {gt: {now: true}}` |

`required: true` 与形状规则**可以放心叠加**：protovalidate 在 `required` 失败时会**短路**，同一字段的其余规则不再评估，所以空值只报一条违规（文案取扩展里的描述），不会重复。

**什么时候才用 CEL**：标准规则表达不了的约束才退回 `cel: {id, message, expression}`（`expression` 返回 `true` 表示通过），例如「FieldMask 至少选中一项」`this.paths.size() > 0`、跨字段的「start 必须早于 end」（写在 `option (buf.validate.message) = {cel: {...}}`）。CEL 规则自带 `message`，失败时用的就是它；同一个字段若也挂了 `(validate.v1.error_message)`，扩展优先（`update_mask` 两处写成同一句话，正是为了让「未传 mask」和「传了空 mask」两种失败对外表现一致）。

**PATCH 语义**：`UpdateProfileRequest` 的可更新字段（`phone` / `nickname` / `avatar`）标 `ignore: IGNORE_IF_ZERO_VALUE` —— 未被 mask 选中的字段是零值，直接跳过规则，长度约束只在客户端真的传了值时生效；`update_mask` 自身要求至少选中一个可更新字段。创建路径（`RegisterRequest`）则相反：`required: true` 加长度规则，必填且形状受限。给新资源加规则时按 RPC 语义选：Create 直接写约束，Update/PATCH 请求的字段加 `ignore: IGNORE_IF_ZERO_VALUE`。

注意校验层的"零值跳过"只管**规则**，不等于落库语义：`UpdateProfile` 把 mask 选中的字段集合一路传到 repo（`biz.UserField`），repo 只写这些列 —— 所以「选中 `nickname` 且值为空」是**清空昵称**，会真的写进库；未选中的列保持原值。若反过来靠"值非空才写"来推断意图，清空操作就会变成静默 no-op：接口返回 200，响应体却还是旧值。

**边界在哪**：protovalidate 只管**请求形状**（格式、长度、范围、必填）。跨请求 / 需要查库的规则（用户名是否已存在、旧密码是否正确、状态机是否允许这次流转）属于领域规则，留在 `biz` 层，用 `errors.Conflict` / `errors.BadRequest` + `error_reason.proto` 的枚举抛出；`service` 层只做 DTO ↔ DO 转换，不写业务判断。

### API 文档（Swagger / OpenAPI）

文档由 proto **生成**、由服务**自己托管**：`make api` 按 `api/<domain>` 各产出一份 spec 并 `go:embed` 进二进制；UI 用 Go 生态的事实标准 `swaggo/http-swagger`，它托管内嵌的官方 `swagger-ui-dist` 资源 —— **不走 CDN、离线可用**，也不用手写 swaggo 注解（文档仍从 proto 生成）：

```
api/<domain>/**.proto ──make api──► pkg/docs/specs/<domain>/openapi.yaml ──go:embed──► 二进制
                                                                                        │
                             GET /openapi.yaml ◄────────────────────────────────────────┤  (注入 securitySchemes + 重写 servers)
                             GET /swagger/  (UI) ◄──────────────────────────────────────┘  (http-swagger + 内嵌静态资源)
```

- **`make api` 按 domain 产出 spec**：`buf.gen.yaml` 里的 `protoc-gen-openapi` 对每个 `api/<domain>` 目录各跑一次（`--path api/<domain>`，同一次调用顺带重生该 domain 的桩），产出 `pkg/docs/specs/<domain>/openapi.yaml` —— **一个 domain 一份**，而非把整个 `api/` 合并成单份。改 proto → `make api` → 文档自动跟上，**不可能与代码脱节**。
- **每服务只显示自己的接口**：服务在 `http.go` 里用 `docs.Register(srv, "<domain>")` 指名托管自己实现的那份 spec，所以 `user_center` 的 `/swagger` 只列 `api/user` 的 RPC。若把所有 domain 合并成一份，每个服务的 `/swagger` 都会列出别家的接口，"Try it out" 打到本服务没有的路由只会 404。
- **spec 与线上报文严格一致**：生成参数用 `naming=proto` + `enum_type=string`，正好对齐运行时的 protojson（snake_case 字段、枚举按名、int64 为字符串）。用默认 `naming=json` 生成会得到 camelCase 的假文档 —— 这是个很容易踩的坑。
- **一处已知局限**：`protoc-gen-openapi` 会把 `google.api.field_behavior = REQUIRED` 翻译成 schema 的 `required` 列表，但**不会**把 `buf.validate` 的约束（长度 / 格式 / 正则…）翻译成 `minLength` / `format` / `pattern`。也就是说文档里能看到"哪些字段必填、是什么类型"，看不到"长度和格式的具体门槛"—— 那部分以 `.proto` 为准，实际请求也会被服务端按规则拒绝（见「参数校验」）。
- **`go:embed` 内嵌**：`pkg/docs/docs.go` 把 yaml 编进二进制，运行时不依赖文件系统，镜像里不必再 COPY 一份 spec。UI 的 css/js 同样内嵌在二进制里（`swaggo/files` 打包了官方 `swagger-ui-dist`），整个 `/swagger` 自包含。
- **一行挂载**：`internal/server/http.go` 里一行 `docs.Register(srv, "user")` 就注册了两个路由（`"user"` 是本服务实现的 `api/<domain>` 目录名）。不想要文档的服务删掉这一行即可。

```bash
make api && make run-user-center
open http://localhost:8000/swagger       # 可交互 UI，直接 "Try it out"
curl  http://localhost:8000/openapi.yaml # 原始 spec，导入 Postman / Apifox / Insomnia
```

几个实现细节值得知道：

- **`servers` 会被动态重写**：proto 里的 `google.api.default_host` 会生成 `https://user.example.com` 这类占位 host，直接拿去 "try it out" 会打到不存在的域名。`handleSpec` 按当前请求的 origin（含 `X-Forwarded-Proto` / `X-Forwarded-Host`）重写 `servers`，所以经网关或 TLS 代理访问时也是对的。
- **UI 静态资源内嵌、不走 CDN**：`swaggo/http-swagger` 直接托管 `swaggo/files` 里内嵌的官方 `swagger-ui-dist`（`swagger-ui.css` ≈145 KB、`swagger-ui-bundle.js` ≈1 MB），`/swagger` 完全离线可用、浏览器无需出网，UI 版本也随二进制锁定、可复现。代价是二进制增大约 8 MB（内嵌资源含 source map）—— 用体积换掉 CDN 依赖。
- **受保护接口怎么调**：`protoc-gen-openapi` 不生成 `securitySchemes`，`pkg/docs` 在托管时往 spec 注入一份 `bearerAuth`（HTTP bearer / JWT），挂到 `components.securitySchemes` 与顶层 `security`。于是 UI 用的是 swagger 原生的 **Authorize** 按钮（开了 `persistAuthorization`，刷新/重开浏览器不丢），从 `POST /v1/auth/login` 拿到 `access_token` 填进去即可调需鉴权的 RPC；导入 Postman / Apifox 同样会提示要 token。注入是全局的，公开 RPC（Register/Login/RefreshToken）也会带锁标记，但不填 token 时请求根本不带 `Authorization` 头，而鉴权 selector 对本就不保护的路由会忽略该头，无副作用。
- **`/swagger` 是裸 handler**：绕过 kratos 中间件链 —— 不套信封、不做鉴权、不计入限流。它是**开发/内网工具**，不要暴露到公网边缘。
- **文档为什么在 `pkg/docs`（根模块）**：`go:embed` 不能跨模块边界，而 `api/` 属根模块、`app/user_center/` 是嵌套模块；所有 domain 的 spec 统一放在根模块的 `pkg/docs/specs/` 下（`embed.FS` 嵌入整棵目录树，新增 domain 无需改代码），由服务 import 后指名托管自己那份。

### 鉴权（JWT）

鉴权中间件本体在 `pkg/middleware/auth.go`（`TokenVerifier` 接口 + `TokenAuth`：抽取 bearer、校验、把 `AuthClaims` 注入 `context`），令牌引擎在 `pkg/jwt`（HS256 签发/校验）。`internal/server/auth.go` 只保留**服务专属**部分：一个把 `biz.AuthUsecase` 适配成 `TokenVerifier` 的小结构，加一个 `selector` 做**选择性**鉴权 —— `UserService` 全部 + `AuthService` 的 `Logout`/`ChangePassword` 需要 `Authorization: Bearer <access_token>`；`Register`/`Login`/`RefreshToken` 放行。校验通过后下游用 `pkgmw.UserIDFromContext(ctx)` 取当前用户。未带 / 无效 token 返回 `code=401`（reason `AUTH_UNAUTHORIZED` 只进日志，不进 body）。新增受保护 RPC 时，在 `authMiddleware` 的 `selector` 里加 `Prefix`/`Path` 即可。

### 请求日志与链路追踪

- `pkg/middleware/logging.go`（`RequestLogger`）：每请求记一行 `kind/operation/code/reason/latency`，**不打印请求体**（避免明文密码入日志）；出错时升为 Error 级并带 message。未使用 kratos 自带 `logging.Server()`，因其 `%+v` 会 dump 出密码。
- `tracing.Server()`（contrib/otel）已挂载，日志自动携带 `trace_id`/`span_id`。**默认无 exporter（noop）**，接入时在 `main.go` 设置全局 `otel.SetTracerProvider(...)`（OTLP/Jaeger 等）即可生效，无需改动中间件。

## 数据库接入（ent × mysql / postgres）

存储层只有一套实现：**ent**（`app/user_center/internal/data/ent/`）—— schema 用 Go 写，客户端、查询构造器、迁移描述全部生成，手写的只有 schema、repo 和 DO↔PO 转换。完整流程（建表 → 生成 → 接线 → 本地开发 → 生产版本化迁移）见 **[docs/ent.md](docs/ent.md)**，这里只讲配置。

### 驱动与 DSN

| `driver` | 底层依赖 | `source`（DSN）示例 | 备注 |
|---|---|---|---|
| `mysql` | `go-sql-driver/mysql` | `root:123456@tcp(127.0.0.1:3306)/user_center?parseTime=true&loc=Local` | **默认**，`deploy/` 的 compose 起的就是它 |
| `postgres` | `jackc/pgx/v5/stdlib` | `postgres://postgres:123456@127.0.0.1:5432/user_center?sslmode=disable` | 亦接受别名 `postgresql` |

> ent 把 `database/sql` 驱动名与 dialect 常量分开映射：配置填 `postgres`，实际 `sql.Open` 用 `pgx`。映射写在 `internal/data/ent/data.go` 的 `openClient` 里，填错驱动名会直接报 `unsupported ent driver`。

> **没有 sqlite**：模板不再提供纯 Go 的 sqlite 驱动，本地开发统一用 `make middleware-up` 起 MySQL —— 少一个"本地能跑、生产不能跑"的方言差异（sqlite 没有真正的并发写、类型系统也宽松得多）。

### 换到 PostgreSQL

改 `configs/user_center.yaml` 的两行即可，代码不用动（pgx 驱动已经 import）：

```yaml
data:
  database:
    driver: postgres
    source: '${DB_SOURCE:postgres://postgres:123456@127.0.0.1:5432/user_center?sslmode=disable}'
```

配置里已经以注释形式给出了这段，取消注释就能用。

### debug 与 auto_migrate

```yaml
    debug: true        # 打印每条 SQL，只在开发环境开
    auto_migrate: true # 启动时 ent Schema.Create；生产关掉
```

`auto_migrate` 调的是 ent 的 `Schema.Create`：建缺失的表和列，但**默认不删**已废弃的列/索引、也不做有损的类型变更。它是本地开发的便利，**生产必须关掉** —— 应用启动时改表无法评审、无法灰度，多副本并发启动还会互相打架。生产用 Atlas 做版本化迁移，迁移 SQL 落在 `deploy/script/migrations/`，详见 [docs/ent.md 第 6 步](docs/ent.md)。

凭据不要写进版本库 —— 用 `${DB_SOURCE:...}` 占位符（对应 `KRATOS_DB_SOURCE`）或 Nacos 配置中心注入，见「配置说明」。

## 服务注册、发现与配置中心（Nacos）

模板统一使用 **Nacos** —— kratos 生态中最主流、也是唯一同时提供注册与热更新配置的后端。`pkg/nacos` 封装了两块能力：

- `nacos.NewRegistry(opts)` 返回同时实现 kratos `registry.Registrar` + `registry.Discovery` 的对象，服务端用于注册、网关用于发现。
- `nacos.NewConfigSource(opts, dataID)` 返回 kratos `config.Source`，拉取配置中心同名 dataID 并 Watch 热更新；空内容不遮蔽本地 yaml。

```go
// cmd/*/main.go 中直接构造，无需额外抽象层：
reg, err := nacos.NewRegistry(nacos.Options{
    Address:     bc.Registry.GetAddress(),
    NamespaceID: bc.Registry.GetNamespaceId(),
    Group:       bc.Registry.GetGroup(),
    Username:    bc.Registry.GetUsername(),
    Password:    bc.Registry.GetPassword(),
})
```

- Kratos 自动把每个 transport 注册为独立服务：`<name>.http`、`<name>.grpc`；网关按 `<service>.http` 解析后端。
- `registry.address` 留空时，服务端不注册也不拉取远程配置（本地零依赖）；网关因必须发现后端，会在启动时报错退出。

## 分布式 ID（pkg/snowflake）

`pkg/snowflake` 实现 Twitter Snowflake 算法，就地生成 64-bit 单调递增、集群唯一的 ID，适用于主键、订单号、消息 ID 等场景。无需任何外部协调，性能约每节点 409.6 万/秒。

### 位布局

```
 0 | timestamp (41 bits) | node id (10 bits) | sequence (12 bits)
   |  ms since Epoch     |  0..1023          |  0..4095 per ms
```

- **timestamp**：以 `DefaultEpoch`（2020-01-01 UTC）为起点的毫秒数，41 bits 可用至 ~2089 年。
- **node id**：集群内节点编号，最多 1024 个节点，需确保**全局唯一**（重复将造成 ID 碰撞）。
- **sequence**：同一毫秒内的自增序号，耗尽后 Generate 会自旋至下一毫秒。

### 用法

```go
node, err := snowflake.NewNode(cfg.NodeId)   // 从配置读取节点号
if err != nil { return err }

id, err := node.Generate()
if err != nil { return err }                // 时钟大幅回拨时返回 ErrClockBackwards

user.ID  = id.Int64()                       // 存入 BIGINT
reply.Id = id.Int64()                       // DTO 字段声明为 int64，protojson 自动输出 JSON 字符串，JS 端不丢 2^53 精度
```

> **user_center 已就地接入**：`pkg/idgen` 定义 `Generator` 接口 + 雪花实现，`internal/biz` 的 `NewIDGeneratorFromConf` 依据 `configs/user_center.yaml` 的 `snowflake.node_id` 构建节点；`AuthUsecase.Register` 在写库前为 `User.ID` 赋值。DTO（`api/user/v1`）的 `id`/`user_id` 声明为 `int64`，ent 主键是**不自增**的 BIGINT（`IDMixin` 用 `entsql.Annotation{Incremental: &false}` 关掉自增，否则数据库会和应用抢着发号）。

### 节点号分配策略

| 方式 | 适用 | 备注 |
|---|---|---|
| 静态配置（推荐） | 固定集群 | 每实例从 yaml/env 读取自己的 ID，运维统一分配 |
| `NewNodeFromHostname()` | 本地开发 | FNV 哈希 hostname 到 [0,1023]，便利但**不保证唯一**，勿用于生产 |
| 外部协调器 | 弹性集群 | 启动时从 etcd/zk 租一个 ID，本包不管，插到 `NewNode(id)` 即可 |

### 时钟回拨保护

- 小幅回拨（≤ `driftTolerance` = 5ms）：自旋等待时钟追上，不报错。
- 大幅回拨：返回 `ErrClockBackwards` 而非冒险发重。NTP step、VM 迁移都可能出现，调用方需处理（记录日志 / 降级 / 重试）。

### JSON 精度

ID 实现了 `encoding.TextMarshaler`，`json.Marshal` 会自动输出为十进制字符串。Proto 字段声明为 `int64` 即可，protojson 同样输出字符串，端到端保持精度。

## Gateway 网关

`app/gateway` 基于 Kratos v3 原语自研的轻量网关（官方 go-kratos/gateway 仅兼容 v2，故不复用）：

- **路由**：`configs/gateway.yaml` 的 `gateway.routes`，按 `path_prefix` 前缀匹配，`service` 填服务名（自动解析为注册中心的 `<service>.http`），可选 `rewrite_prefix` 重写前缀
- **发现**：每条路由持有独立 watcher，实例变更实时刷新
- **负载均衡**：kratos selector（默认 random；可换 `p2c` 等策略）
- **CORS**：`gateway.cors` 配置预检与跨域头；生产请显式列出 origins，勿用 `"*"` + `allow_credentials: true`
- **边缘限流**：`middleware.ratelimit` 固定 token bucket，以 HTTP filter 形式挂在最内层（详见「限流与熔断」）
- **每后端熔断**：`middleware.circuit_breaker` 基于 sony/gobreaker，每条路由一个独立熔断器，后端持续 5xx 即开路快速失败
- **容错**：无健康实例返回 `503`；后端连接失败返回 `502`；熔断开路返回 `503`；限流命中返回 `429`。响应体与服务端信封同形（`{"code":…,"message":…,"data":null}`），但**保留真实 HTTP 状态码** —— 网关是代理，藏起状态码会让缓存、浏览器和重试逻辑失效

新增后端只需在 `routes` 下加一条：

```yaml
gateway:
  routes:
    - path_prefix: /v1/orders
      service: order        # 自动发现注册中心里的 order.http
```

## 限流与熔断

`pkg/middleware` 与网关 proxy 提供两类稳定性能力，**默认全部关闭**，按需在 `configs/*.yaml` 打开。设计上遵循行业通行的分工：**边缘固定配额限流 + 后端自适应过载保护 + 客户端侧熔断**。

### 限流：服务端 vs 网关端

| | 服务端（user_center） | 网关端（gateway） |
|---|---|---|
| 配置 | `middleware.ratelimit` | `middleware.ratelimit` |
| 形态 | kratos `middleware.Middleware`（走生成的 proto handler 链） | HTTP `filter`（`khttp.Filter`） |
| 算法 | `bbr`（kratos 自适应，默认）或 `token`（固定 qps/burst） | `token`（固定 qps/burst） |
| 触发 | HTTP `200` + `{"code":429,"message":"…","data":null}`（套信封） | HTTP `429` + `{"code":429,"message":"rate limit exceeded","data":null}` |

**为什么形态不同**：kratos 的 `http.Middleware(...)` 链是在**生成的 proto 路由 handler 内部**通过 `ctx.Middleware(...)` 调用的；网关的反向代理路由用 `HandlePrefix` 注册的是**裸 handler**，从不调用它，因此**绕过了 kratos 中间件链**，只能用 `khttp.Filter` 包裹。这也是网关端只提供 token bucket 的原因 —— kratos 的 BBR 构造器在 internal 包、且 `ratelimit.Limiter` 接口无 request 上下文，无法作为独立 limiter 挂到 filter 上。

**为什么算法分工**：BBR（Bilibili Backend Ratelimit）从观测到的延迟/CPU 自适应收敛阈值，适合保护单个后端服务不被打垮；边缘网关面对的是聚合流量，用固定 token bucket 给出可预期的配额更合适。二者可叠加：网关削掉总量洪峰，各服务再用 BBR 兜住自身过载。

```yaml
# 服务端：configs/user_center.yaml
middleware:
  ratelimit:
    enabled: true
    type: bbr        # bbr(默认,自适应) | token(固定配额)
    qps: 100         # token only
    burst: 200       # token only（0 时默认等于 qps）

# 网关端：configs/gateway.yaml
middleware:
  ratelimit:
    enabled: true
    qps: 1000        # 整网关聚合 QPS
    burst: 2000
```

> 限流器是 **per-server 全局**（kratos `ratelimit.Limiter` 接口无 ctx），即一个服务/网关共享一个桶，而非按操作或按客户端。需要更细粒度（按路由/按租户）的限流应放到专门的边缘组件。

### 熔断：网关 per-upstream（sony/gobreaker）

kratos v3 只提供**客户端侧**熔断中间件（无 `Server()`，内部 SRE 包不可外部 import）。本模板真实的"客户端路径"是网关的反向代理，因此熔断落在 `app/gateway/internal/proxy`：**每条路由一个独立 `gobreaker.CircuitBreaker`**，用后端返回的 HTTP 状态码驱动。

```yaml
# configs/gateway.yaml
middleware:
  circuit_breaker:
    enabled: true
    max_requests: 5     # half-open 时放行的探测请求数
    interval: 30s       # closed 状态计数窗口
    timeout: 30s        # open 后多久转 half-open 探测恢复
    failure_ratio: 0.6  # 触发开路的失败率阈值(0..1)
    min_requests: 20    # 达到该样本量后失败率才可能触发开路
```

- **失败判定**：只有后端返回 `5xx` 记为失败；发现空档（无健康实例）返回 `503` 但**不计入失败**，避免注册抖动误开熔断。
- **开路行为**：熔断器 open 时直接返回 `503`（`{"code":503,"message":"upstream circuit open","data":null}`），不再压到病态后端；`timeout` 后转 half-open 放行 `max_requests` 个探测，成功即闭合。
- **隔离性**：一个后端熔断不影响其它路由 —— 每条 route 独立实例，配置共享。

## 新增资源标准流程

以新增 `order` 资源为例（单服务内）：

1. **DTO**：在 `api/<domain>/v1/` 定义 proto（`<resource>.proto` + 扩充 `error_reason.proto`），每个 RPC 一对独立的 `<Rpc>Request`/`<Rpc>Response`（响应包装资源，如 `CreateOrderResponse{user}` 形态），字段用 `buf.validate` 标准规则声明校验、并用 `(validate.v1.error_message)` 写清失败文案，`make api`（顺带刷新 Swagger spec）；`buf lint` 默认规则必须零告警
2. **DO**：`internal/biz/order.go` 定义 DO、`OrderRepo` 接口、`OrderUsecase`，错误用 `errors.NotFound/BadRequest` + error reason 枚举
3. **PO**：`internal/data/ent/schema/order.go` 写 schema → `make ent` → `internal/data/ent/order_repo.go` 实现 `biz.OrderRepo`（含 `toBiz` 转换与错误映射），详见 [docs/ent.md](docs/ent.md)
4. **service**：`internal/service/order.go` 做 DTO ↔ DO 转换；在 `internal/server` 注册 HTTP/gRPC service；若为受保护资源，在 `auth.go` 的 `authMiddleware` selector 里登记 `Prefix`
5. **重新注入**：`make wire`（或直接 `make generate` = `make ent` + `make wire`）
6. **网关路由**（可选）：`configs/gateway.yaml` 加一条 route

分层契约（import 方向、模型边界）详见 [AGENTS.md](AGENTS.md)。

## 部署

### deploy/ 目录

部署物料集中在 `deploy/`，不散落在仓库根：

```
deploy/
  middleware/                      中间件栈（本地/自托管）
    docker-compose.middleware.yaml MySQL + Redis + Nacos（默认）；ES + Kibana（search profile，选配）
    .env.example                   所有可调项样例（含密钥生成命令）；复制为 .env 后按需改（.env 已 gitignore）
    mysql-init/                    MySQL 首次启动自动执行的 *.sql / *.sh
    README.md                      中间件部署说明
  business/                        业务服务部署（gateway + user_center + 你孵化的服务）
    docker-compose.app.yaml        示例：构建并运行业务服务，接入中间件网络
    k8s/user_center.yaml           示例：单服务 ConfigMap + Secret + Deployment + Service 模板
    README.md                      业务服务部署说明
  script/                          SQL 物料（当前为空占位）
    migrations/                    Atlas 生成的增量迁移（*.sql + atlas.sum），随代码提交
                                   —— 首次 `atlas migrate diff` 时创建
    schema.sql                     可选：某版本的全量 DDL，用于新环境初始化或 DBA 评审
```

- **中间件**：`make middleware-up` / `make middleware-down`（ES + Kibana 用 `make middleware-search-up`）。这份 compose 面向本地开发，凭据、端口与 `configs/*.yaml` 的默认值对齐（都写成 `${VAR:-default}`，不放 `.env` 也能跑）；生产环境请用自己的托管服务或改造后的清单。详见 [deploy/middleware/README.md](deploy/middleware/README.md)。
- **业务服务**：用仓库 `Dockerfile`（`SERVICE` 构建参数）打镜像，`deploy/business/` 给了 Compose 与 K8s 清单模板。详见 [deploy/business/README.md](deploy/business/README.md)。
- **迁移 SQL**：由 Atlas 生成到 `deploy/script/migrations/`，在发布**之前**独立执行（`atlas migrate apply`）。`deploy/script/schema.sql` 可放全量 DDL 供新环境初始化或 DBA 评审。完整流程见 [docs/ent.md](docs/ent.md)。

### 服务镜像

`Dockerfile` 参数化构建任意服务：

```bash
# 构建服务镜像
docker build --build-arg SERVICE=app/user_center/cmd/user_center -t user_center .
# 构建网关镜像
docker build --build-arg SERVICE=app/gateway/cmd/gateway -t gateway .

# 运行（挂载配置目录）
docker run -p 8000:8000 -p 9000:9000 \
  -v $(pwd)/configs:/data/configs \
  user_center -conf /data/configs/user_center.yaml

docker run -p 8080:8080 \
  -v $(pwd)/configs:/data/configs \
  gateway -conf /data/configs/gateway.yaml
```

镜像保持 `CGO_ENABLED=0` 静态构建（MySQL / PostgreSQL 驱动都是纯 Go）。OpenAPI spec 已 `go:embed` 进二进制，容器里不需要额外 COPY 文档文件。

## Make 速查

`make`（或 `make help`）会列出全部目标及其说明：

| 命令 | 作用 |
|---|---|
| `make init` | 安装 buf、wire CLI（代码生成工具）；不含子模块初始化 |
| `make api` | `pkg/validate/v1` 扩展 + `api/**.proto` → Go/gRPC/HTTP 桩 + `pkg/docs/specs/<domain>/openapi.yaml`（每 domain 一份） |
| `make config` | 各服务 `internal/conf/v1/*.proto` → `*.pb.go` |
| `make ent` | 改 ent schema 后重新生成 ORM 代码 |
| `make wire` | 改 ProviderSet / 构造函数签名后重新生成 `wire_gen.go` |
| `make generate` | `ent` + `wire`（改完 biz/data 的常规收尾） |
| `make all` | `api` + `config` + `generate`（全量重生成） |
| `make build` | 编译两个模块的二进制到 `bin/` |
| `make test` | 跑两个模块的全部测试 |
| `make middleware-up` / `middleware-down` | 起 / 停本地中间件（`deploy/middleware/docker-compose.middleware.yaml`） |
| `make middleware-search-up` | 额外起 Elasticsearch + Kibana（`search` profile） |
| `make run-user-center` / `make run-gateway` | 本地运行服务 / 网关 |
| `make tidy` | 在不破坏 workspace 的前提下清理 `go.mod` / `go.sum` |

> 所有目标在 macOS/Linux（POSIX sh）与 Windows（原生 GNU make，配方经 cmd.exe 执行）下均可运行：文件操作只经 `$(MKDIR)`/`$(MOVE)`/`$(RM_RF)` 三个按 OS 分支的助手，迭代交给 make 自身（`$(wildcard)` + 子目标）而非 shell 循环。Windows 建议用 scoop/choco 安装 `make`（或 `mingw32-make`）；WSL 等同 Linux。

> 生成的文件（`*.pb.go`、`*_grpc.pb.go`、`*_http.pb.go`、`wire_gen.go`、ent 生成代码、`specs/<domain>/openapi.yaml`）**都提交进仓库**，所以源文件改动与重生成结果属于同一个 commit；永远不要手改生成文件。

## 文档索引

| 文档 | 内容 |
|---|---|
| [AGENTS.md](AGENTS.md) | 分层契约：service/biz/data 谁能 import 谁、DTO/DO/PO 边界、新增资源清单 |
| [docs/ent.md](docs/ent.md) | ent 全流程：写 schema → 生成 → repo → 接线 → 本地开发 → 生产版本化迁移 |
| [deploy/middleware/README.md](deploy/middleware/README.md) | 中间件栈部署：MySQL/Redis/Nacos（+ 选配 ES/Kibana）、`.env` 覆盖、开启鉴权 |
| [deploy/business/README.md](deploy/business/README.md) | 业务服务部署：构建镜像、Compose 与 K8s 清单模板、配置接线注意点 |
| `http://localhost:8000/swagger` | 运行时的可交互 API 文档 |
