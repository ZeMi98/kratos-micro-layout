# kratos-micro-layout

基于 [Kratos v3](https://go-kratos.dev) 的微服务 monorepo 模板：一个 HTTP 网关 + 一个用户中心服务模板 + 共享基础库（日志、Nacos、传输中间件套件、JWT 令牌引擎、ID 生成、雪花算法）。

数据库保留 ent / gorm 两套实现，按需删减；日志统一用标准库 **log/slog**（Text/JSON + lumberjack 轮转）；服务注册 / 发现 / 配置中心统一由 **Nacos** 承担（kratos 生态中最主流、也是唯一同时提供注册与热更新配置的后端），网关内建**限流 + 熔断**，分布式 ID 由 `pkg/snowflake` 就地生成，全部配置集中在根目录 `configs/`。

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
                        │             │  服务端限流(BBR/token) + 鉴权 + 校验
                        └──────┬──────┘
                               │
                 ┌─────────────┴─────────────┐
                 │  biz ─► data (ent|gorm)   │  sqlite(默认)/mysql/postgres
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
  internal/conf/             配置 proto（make config 生成）
  internal/server/           HTTP/gRPC server 装配 + 鉴权 selector（中间件本体在 pkg/middleware）
  internal/service/          DTO ↔ DO 转换层
  internal/biz/              领域模型、用例、Repo 接口、错误；JWT/ID 引擎的薄 conf provider
  internal/data/             薄入口：ProviderSet 指向所保留的 ORM
    ent/                     Ent 实现（schema + 生成代码 + repo）
    gorm/                    GORM 实现（model + repo）
app/gateway/                 网关（无 biz/data，手工装配）
  internal/proxy/            服务发现 + selector 负载均衡 + 反向代理 + 每后端熔断
  internal/server/           监听器、CORS、边缘限流 filter、路由注册
pkg/log/                     日志构建：标准库 log/slog（Text/JSON）+ lumberjack 文件轮转
pkg/middleware/              可复用传输中间件：鉴权、编解码(信封+protojson)、日志、校验、限流
pkg/jwt/                     HS256 令牌引擎（Manager/Claims）：签发与校验 access/refresh token
pkg/idgen/                   ID 抽象：Generator 接口 + 雪花实现（int64 主键）
pkg/nacos/                   Nacos 注册/发现、配置中心 Source 封装
pkg/snowflake/               Twitter Snowflake 分布式 ID 生成器（64-bit，无协调、可排序）
configs/user_center.yaml     每服务一份配置，集中存放
configs/gateway.yaml
Makefile / buf*.yaml         代码生成入口
Dockerfile                   参数化构建任意服务的镜像
```

## 多模块与 go.work 工作区

本仓库是一个 **Go workspace（多模块）**：根模块承载 `api/`、`pkg/` 与 `app/gateway/`，而 `app/user_center/` 是一个**嵌套的独立模块**（自带 `go.mod`，同时作为服务模板托管在 [kratos-micro-sub-service-layout](https://github.com/ZeMi98/kratos-micro-sub-service-layout)）。根目录的 `go.work`（**已提交**）把两者纳入同一工作区：

```
go 1.26.0

use (
	.                 # 根模块：api/、pkg/、app/gateway/
	./app/user_center # 嵌套的服务模块
)
```

- **为什么 user_center 必须有 `go.mod`**：`kratos new --nomod -r <repo>` 生成新服务时，CLI 会读取模板 `go.mod` 的 module path 作为替换基准；缺了它直接报错。所以 `app/user_center/go.mod` 不能删。
- **为什么它是最小的**：`app/user_center/go.mod` 只有 `module` + `go` 两行，不列任何 `require`。它对第三方依赖（kratos、ent/gorm、pgx…）以及对根模块 `api/`、`pkg/` 的引用，全部经 `go.work` 从**根模块的构建列表**解析 —— 服务模板因此不必重复维护一份依赖清单。
- **对命令行的影响**：嵌套模块会被父模块的裸 `./...` 排除，故 `Makefile` 的 `build`/`test` 显式带上 `./app/user_center/...`；工作区内 `cd app/user_center && go build ./...` 与根目录 `go build ./app/user_center/...` 均可正常解析。
- **加依赖只在根目录做**：根模块自身并不 import ent/gorm/pgx（只有 user_center 用），因此**不要在根目录裸跑 `go mod tidy`** —— 它会把“仅 user_center 使用”的依赖当作无用而剪掉，破坏工作区解析。新增依赖统一用 `go get <module>` 加到根 `go.mod`（`make generate` 已刻意不含 tidy）。
- `go.work.sum` 是工作区派生的校验文件，已在 `.gitignore` 忽略；`go.work` 本身必须提交，否则新克隆无法解析 user_center。

## 快速开始

### 1. 用模板创建新项目

```bash
# 安装 Kratos CLI（若未安装）
go install github.com/go-kratos/kratos/cmd/kratos/v3@latest

# 基于本模板创建项目（替换 REPO 为你的 fork 地址）
kratos new my-project -r https://github.com/ZeMi98/kratos-micro-layout.git
cd my-project
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
3. 在 `buf.yaml` / `buf.gen.config.yaml` 中登记新模块路径

### 3. 本地运行（无需 Nacos）

模板默认 `registry.address` 为空 —— Nacos 整链路关闭，开箱即跑：

```bash
make run-user-center   # sqlite 自动建库建表，监听 :8000/:9000

# 冒烟
curl -X POST localhost:8000/v1/auth/register -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"secret123","email":"alice@example.com"}'
curl -X POST localhost:8000/v1/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"alice","password":"secret123"}'
```

### 4. 完整模式（带注册中心 + 网关）

```bash
# Nacos（同时承担注册中心与配置中心）
docker run -d --name nacos -p 8848:8848 -p 9848:9848 -e MODE=standalone nacos/nacos-server
#   configs/*.yaml:
#     registry: { address: 127.0.0.1:8848 }

make run-user-center   # 服务注册为 user_center.http / user_center.grpc
make run-gateway       # 网关监听 :8080，经 Nacos 发现后端

curl localhost:8080/v1/users -H 'Origin: http://localhost:3000'   # 经网关 + CORS
```

> `registry.address` 非空时，各服务同时拉取 Nacos 配置中心的同名 dataID（`<service>.yaml`）并热更新；留空时本地 yaml 即唯一配置来源。

## Proto 管理与 buf 缓存

proto 依赖（`googleapis` 等）通过 [buf](https://buf.build) 管理，**没有 `third_party/` 目录**：

- `buf.yaml` 声明 workspace 模块（`api`、各服务 `internal/conf`）与 BSR 依赖
- `buf.lock` 锁定依赖版本
- 依赖的 proto 只会被拉取到**本地缓存目录**，构建时从缓存读取：
  - Linux：`~/.cache/buf/v3`
  - macOS：`~/.cache/buf/v3`
  - 可用 `BUF_CACHE_DIR` 环境变量重定向；删除该目录即强制重新拉取

```bash
make init          # 安装 buf 与 wire CLI
buf dep update     # 按 buf.yaml 拉取/更新依赖（写入 buf.lock 与缓存）

make api           # api/**.proto   → go / grpc / http / openapi.yaml
make config        # 各服务 internal/conf → *.pb.go
```

生成模板分三个文件：`buf.gen.yaml`（api 公共契约）、`buf.gen.config.yaml`（user_center 配置）、`buf.gen.gw.yaml`（gateway 配置）。
注意 gateway 的配置 proto 使用独立 package `kratos.gateway`，避免与各服务模板的 `kratos.api` 包在同一次编译中冲突。

## 配置说明

每个服务一份 `configs/<service>.yaml`，集中存放、以服务名命名。加载顺序（见各 `cmd/*/main.go` 的 `loadConfig`）：

1. 本地文件 `configs/<service>.yaml`（永远生效，兜底）
2. 当 `registry.address` 非空：Nacos 配置中心同名 dataID（`<service>.yaml`）合并覆盖，支持热更新
3. `${VAR:default}` 占位符：kratos 默认 resolver 在合并后解析，从环境变量取值

> **环境变量注入的正确姿势**：kratos 的 env source 只剥离 `KRATOS_` 前缀（及一个前导下划线），**不会**把键小写化、也**不会**把 `_` 映射成 `.`。因此 `KRATOS_AUTH_JWT_SECRET` 只会变成顶层键 `AUTH_JWT_SECRET`，无法直接覆盖嵌套的 `auth.jwt_secret`。要让某个值走环境变量，用占位符引用它 —— `jwt_secret: ${AUTH_JWT_SECRET:change-me-in-production}`，再设 `KRATOS_AUTH_JWT_SECRET=...`（前缀被剥离后正好命中占位符查找的键）。模板里的 `auth.jwt_secret` 就是这么做的。

> Duration 一律使用 protojson 秒格式（如 `7200s`），不能写 `2h`。

关键项速览（`configs/user_center.yaml`）：

| 键 | 说明 |
|---|---|
| `data.database.orm` | `ent` 或 `gorm`，须与保留的 `internal/data` 子包一致 |
| `data.database.driver` | `sqlite`（纯 Go、免 CGO，默认）、`mysql` 或 `postgres`（详见「数据库接入」） |
| `auth.jwt_secret` / `*_ttl` | JWT 密钥（用 `${AUTH_JWT_SECRET:...}` 注入）与 access/refresh token 有效期 |
| `log.level` / `output` / `format` | 日志级别、输出目标（stdout/stderr/file）与编码（text/json）；引擎固定为标准库 log/slog |
| `registry.address` | Nacos 服务器地址；留空则禁用注册/发现/远程配置（本地零依赖默认） |
| `registry.namespace_id` / `group` | Nacos 命名空间与分组，留空即用 public / DEFAULT_GROUP |
| `middleware.ratelimit` | 服务端限流：`enabled` / `type`(bbr\|token) / `qps` / `burst` |

网关（`configs/gateway.yaml`）另有 `middleware.circuit_breaker`（每后端熔断），详见「限流与熔断」。

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
| `validate.go` | `ProtoValidator` | 供 kratos `validate.Validator(...)` 使用的 protovalidate 校验器 |

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
	authMiddleware(authUC),                   // 选择性鉴权（selector 白名单）
	validate.Validator(pkgmw.ProtoValidator), // 最内层：贴近 handler 校验入参
)
```

HTTP 额外用 `http.RequestDecoder/ResponseEncoder/ErrorEncoder` 挂 protojson 与信封；gRPC 保留原生 status code、不套信封，其余中间件与 HTTP 完全一致。

**接入一个新中间件**：把可复用逻辑写进 `pkg/middleware/<name>.go`（导出一个返回 `middleware.Middleware` 的构造器），再在 `http.go` 和 `grpc.go` 的 `mws` 切片里按期望顺序 `append` —— 两个 transport 要同步改以免行为漂移。服务专属、不适合下沉的部分（把 `biz.AuthUsecase` 适配成 `TokenVerifier`、`selector` 白名单）留在 `internal/server/auth.go`。

### 统一响应信封

所有 HTTP 响应（成功与失败）都包成同一结构，**HTTP 状态码恒为 `200`**，业务结果看 `code`：

```json
{ "code": 0, "message": "", "reason": "", "data": { }, "metadata": {} }
```

| 字段 | 成功 | 失败 |
|---|---|---|
| `code` | `0` | kratos/gRPC 状态码（`400`/`401`/`404`/`500`…） |
| `reason` | 空 | API 错误枚举（`VALIDATOR`/`AUTH_UNAUTHORIZED`/`USER_NOT_FOUND`…） |
| `message` | 空 | 错误描述 |
| `data` | 资源对象 | `null` |

实现在 `pkg/middleware/codec.go`（`ResponseEncoder`/`ErrorEncoder`）。想恢复语义化 HTTP 状态码，改 `writeEnvelope` 里的 `WriteHeader` 一行即可。**gRPC 不套信封**，沿用原生 status code。

### protojson 序列化

请求体与响应体都用 [protojson](https://protobuf.dev/reference/go/faq#json) 编解码（覆盖 kratos v3 默认的 `encoding/json`），与 gRPC-Gateway / Google API 生态一致：

- 时间戳是 RFC 3339 字符串（`"2026-09-02T08:46:40Z"`），而非 `{"seconds":…,"nanos":…}`
- 枚举用名字（`"USER_STATUS_ACTIVE"`），而非数字
- 字段名 snake_case（`UseProtoNames`），与 `.proto`、`openapi.yaml` 对齐
- int64 编码为字符串，避免 JS 精度丢失

请求侧同样走 protojson（`pkg/middleware` 的 `RequestDecoder`），因此客户端可把响应里的字段原样回传（枚举名、RFC 3339 时间都能被解析）。

### 参数校验（protovalidate）

校验规则用 [protovalidate](https://buf.build/docs/protovalidate) 直接写在 `.proto` 上（`buf.validate` 注解），运行时由 `validate.Validator` 中间件执行 —— **无需代码生成**，HTTP 与 gRPC 共用同一套规则不会漂移：

```proto
string email = 2 [(buf.validate.field) = {
  required: true,
  string: {email: true, max_len: 255}
}];
string password = 3 [(buf.validate.field) = {
  required: true,
  string: {min_len: 8, max_len: 72}   // bcrypt 上限 72 字节
}];
```

校验失败返回 `code=400, reason=VALIDATOR`。共享资源 `User` 的字段用 `ignore: IGNORE_IF_ZERO_VALUE`（而非 `required`），保证 `UpdateUser`（PATCH）局部更新时未传字段不触发规则；创建路径的非空由 `biz` 层兜底。

### 鉴权（JWT）

鉴权中间件本体在 `pkg/middleware/auth.go`（`TokenVerifier` 接口 + `TokenAuth`：抽取 bearer、校验、把 `AuthClaims` 注入 `context`），令牌引擎在 `pkg/jwt`（HS256 签发/校验）。`internal/server/auth.go` 只保留**服务专属**部分：一个把 `biz.AuthUsecase` 适配成 `TokenVerifier` 的小结构，加一个 `selector` 做**选择性**鉴权 —— `UserService` 全部 + `AuthService` 的 `Logout`/`ChangePassword` 需要 `Authorization: Bearer <access_token>`；`Register`/`Login`/`RefreshToken` 放行。校验通过后下游用 `pkgmw.UserIDFromContext(ctx)` 取当前用户。未带 / 无效 token 返回 `code=401, reason=AUTH_UNAUTHORIZED`。新增受保护 RPC 时，在 `authMiddleware` 的 `selector` 里加 `Prefix`/`Path` 即可。

### 请求日志与链路追踪

- `pkg/middleware/logging.go`（`RequestLogger`）：每请求记一行 `kind/operation/code/reason/latency`，**不打印请求体**（避免明文密码入日志）；出错时升为 Error 级并带 message。未使用 kratos 自带 `logging.Server()`，因其 `%+v` 会 dump 出密码。
- `tracing.Server()`（contrib/otel）已挂载，日志自动携带 `trace_id`/`span_id`。**默认无 exporter（noop）**，接入时在 `main.go` 设置全局 `otel.SetTracerProvider(...)`（OTLP/Jaeger 等）即可生效，无需改动中间件。

## 数据库接入（ent / gorm × sqlite / mysql / postgres）

存储层由两个正交维度决定：**ORM 引擎**（`data.database.orm`：`ent` 或 `gorm`）与 **SQL 驱动**（`data.database.driver`：`sqlite` / `mysql` / `postgres`）。两套 ORM 都支持这三种驱动，可自由组合。

### 驱动与 DSN

| `driver` | 底层依赖（ent / gorm） | `source`（DSN）示例 | 备注 |
|---|---|---|---|
| `sqlite` | `modernc.org/sqlite` / `glebarez/sqlite`（均纯 Go、免 CGO） | `user_center.db`（文件路径） | 默认，本地零依赖；ent 侧自动补 `foreign_keys` pragma |
| `mysql` | `go-sql-driver/mysql` / `gorm.io/driver/mysql` | `user:pass@tcp(127.0.0.1:3306)/dbname?parseTime=true` | 生产常用 |
| `postgres` | `jackc/pgx/v5/stdlib` / `gorm.io/driver/postgres` | `postgres://user:pass@127.0.0.1:5432/dbname?sslmode=disable` | 亦接受别名 `postgresql` |

> ent 把 `database/sql` 驱动名与 dialect 常量分开映射：pgx 注册名是 `pgx`、ent dialect 是 `postgres`；纯 Go sqlite 注册名是 `sqlite`、ent dialect 是 `sqlite3`。配置里统一填 `driver: postgres`（或 `mysql` / `sqlite`）即可，映射在 `internal/data/ent/data.go` 内部完成。

以 postgres + gorm 为例，改 `configs/user_center.yaml`：

```yaml
data:
  database:
    driver: postgres
    source: postgres://user:pass@127.0.0.1:5432/user_center?sslmode=disable
    orm: gorm
    debug: false          # true 时打印每条 SQL（仅开发）
    auto_migrate: true    # 启动建表/改表；生产建议关闭，改用受控迁移
```

`debug` / `auto_migrate` 对两种 ORM 都生效：`auto_migrate` 是本地开发的便利（ent 走 `Schema.Create`、gorm 走 `AutoMigrate`），**生产应关闭**，把表结构变更作为独立、可评审的步骤。凭据不要写进版本库 —— 用 `${VAR:default}` 占位符或 Nacos 配置中心注入（见「配置说明」）。

### 切换 ORM（ent ↔ gorm）

两套实现是自包含子包，各带完整 `ProviderSet`。切换 = 删一个目录 + 改一行：

```bash
# ent → gorm
rm -rf app/user_center/internal/data/ent
# 编辑 app/user_center/internal/data/data.go：
#   import ".../internal/data/gorm"
#   var ProviderSet = wire.NewSet(gorm.ProviderSet)
make wire           # 重新生成 wire_gen.go
# 同步修改 configs/user_center.yaml 的 data.database.orm: gorm
```

反向同理。`biz` 层只依赖 `UserRepo` 接口，对切换无感知。

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

> **user_center 已就地接入**：`pkg/idgen` 定义 `Generator` 接口 + 雪花实现，`internal/biz` 的 `NewIDGeneratorFromConf` 依据 `configs/user_center.yaml` 的 `snowflake.node_id` 构建节点；`UserUsecase.CreateUser` 与 `AuthUsecase.Register` 在写库前为 `User.ID` 赋值。DTO（`api/user/v1`）的 `id`/`user_id` 声明为 `int64`，ent / gorm 主键均为**不自增**的 BIGINT。

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

ID 实现了 `encoding.TextMarshaler`，`json.Marshal` 会自动输出为十进制字符串。Proto 字段声明为 `string` 即可端到端保持精度。

## Gateway 网关

`app/gateway` 基于 Kratos v3 原语自研的轻量网关（官方 go-kratos/gateway 仅兼容 v2，故不复用）：

- **路由**：`configs/gateway.yaml` 的 `gateway.routes`，按 `path_prefix` 前缀匹配，`service` 填服务名（自动解析为注册中心的 `<service>.http`），可选 `rewrite_prefix` 重写前缀
- **发现**：每条路由持有独立 watcher，实例变更实时刷新
- **负载均衡**：kratos selector（默认 random；可换 `p2c` 等策略）
- **CORS**：`gateway.cors` 配置预检与跨域头；生产请显式列出 origins，勿用 `"*"` + `allow_credentials: true`
- **边缘限流**：`middleware.ratelimit` 固定 token bucket，以 HTTP filter 形式挂在最内层（详见「限流与熔断」）
- **每后端熔断**：`middleware.circuit_breaker` 基于 sony/gobreaker，每条路由一个独立熔断器，后端持续 5xx 即开路快速失败
- **容错**：无健康实例返回 `503`；后端连接失败返回 `502`；熔断开路返回 `503`；限流命中返回 `429`，均为 JSON 错误体

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
| 触发 | `code=429, reason=RATELIMIT`（套响应信封） | HTTP `429` + `{"code":429,...}` |

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
- **开路行为**：熔断器 open 时直接返回 `503`（`{"code":503,"message":"upstream circuit open"}`），不再压到病态后端；`timeout` 后转 half-open 放行 `max_requests` 个探测，成功即闭合。
- **隔离性**：一个后端熔断不影响其它路由 —— 每条 route 独立实例，配置共享。

## 新增资源标准流程

以新增 `order` 资源为例（单服务内）：

1. **DTO**：在 `api/<domain>/v1/` 定义 proto（`<resource>.proto` + 扩充 `error_reason.proto`），字段用 `buf.validate` 声明校验规则，`make api`
2. **DO**：`internal/biz/order.go` 定义 DO、`OrderRepo` 接口、`OrderUsecase`，错误用 `errors.NotFound/BadRequest` + error reason 枚举
3. **PO**：保留的 ORM 子包中实现 repo（ent：先写 `schema/order.go` 再 `make ent`；gorm：写 model + repo）
4. **service**：`internal/service/order.go` 做 DTO ↔ DO 转换，在 `internal/server` 注册；若为受保护资源，在 `auth.go` 的 `authMiddleware` selector 里登记 `Prefix`
5. **重新注入**：`make wire`
6. **网关路由**（可选）：`configs/gateway.yaml` 加一条 route

分层契约（import 方向、模型边界）详见 [AGENTS.md](AGENTS.md)。

## Docker 部署

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

sqlite 驱动纯 Go 实现，镜像保持 `CGO_ENABLED=0` 静态构建。

## Make 速查

| 命令 | 作用 |
|---|---|
| `make init` | 安装 buf、wire CLI |
| `make api` / `make config` | 生成 api / 各服务配置代码 |
| `make ent` | 重新生成 ent ORM 代码（改 schema 后） |
| `make wire` | 重新生成依赖注入代码 |
| `make build` / `make test` | 构建到 bin/ / 全量测试 |
| `make run-user-center` / `make run-gateway` | 本地运行 |
| `make generate` / `make all` | ORM+DI 重生成 / 全部代码生成 |
