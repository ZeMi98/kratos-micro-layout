# 业务服务部署（deploy/business）

本目录放**业务服务**（`gateway` + `user_center`，以及你按模板孵化的新服务）的部署物料。
中间件（MySQL/Redis/Nacos/ES）不在这里，见 [deploy/middleware](../middleware/README.md)。

```
deploy/business/
  docker-compose.app.yaml   示例：用仓库 Dockerfile 构建并运行 gateway + user_center，接入中间件网络
  k8s/
    user_center.yaml        示例：单服务的 ConfigMap + Secret + Deployment + Service 模板
```

> 两份清单都是**起始模板**，不是开箱即用的生产清单：镜像仓库地址、`registry.address`、
> 凭据、副本数、资源上限都要按你的环境改。

## 1. 构建镜像

仓库 `Dockerfile` 用 `SERVICE` 构建参数编译任意服务（`CGO_ENABLED=0` 静态构建，
OpenAPI spec 已 `go:embed` 进二进制，容器里无需额外 COPY 文档）：

```bash
# 在仓库根目录
docker build --build-arg SERVICE=app/user_center/cmd/user_center -t <registry>/user_center:<tag> .
docker build --build-arg SERVICE=app/gateway/cmd/gateway        -t <registry>/gateway:<tag> .
```

## 2. 用 Docker Compose 部署（本地/单机）

`docker-compose.app.yaml` 会把两个服务加入中间件的网络，因此**先起中间件**：

```bash
make middleware-up
docker compose -f deploy/business/docker-compose.app.yaml up -d --build
```

访问：网关 `http://localhost:8080`，user_center `http://localhost:8000`，
API 文档 `http://localhost:8000/swagger`。

停止：

```bash
docker compose -f deploy/business/docker-compose.app.yaml down
```

## 3. 用 Kubernetes 部署

以 `k8s/user_center.yaml` 为模板，每个服务一份清单：

```bash
# 1) 建密钥（不要把真实值写进 yaml / 提交进仓库）
kubectl create secret generic user-center-secret \
  --from-literal=DB_SOURCE='user:pass@tcp(mysql:3306)/user_center?parseTime=true&loc=Local' \
  --from-literal=AUTH_JWT_SECRET="$(openssl rand -hex 32)"

# 2) 应用清单（ConfigMap + Secret + Deployment + Service）
kubectl apply -f deploy/business/k8s/user_center.yaml
```

## 配置接线（重要）

服务的配置来自 `-conf` 指向的文件；`configs/*.yaml` 用 `${VAR:default}` 占位符支持环境变量注入。
kratos 的 env 源**只剥离 `KRATOS_` 前缀，不做小写化、也不把 `_` 映射成 `.`**，所以：

| 想改的东西 | 能否用 env 覆盖 | 怎么做 |
|---|---|---|
| 数据库 DSN | ✅ | 配置里是 `${DB_SOURCE:...}`，设 `KRATOS_DB_SOURCE` |
| JWT 密钥 | ✅ | 配置里是 `${AUTH_JWT_SECRET:...}`，设 `KRATOS_AUTH_JWT_SECRET` |
| `registry.address` 等嵌套键 | ❌ | 直接改配置文件 / ConfigMap，或走 Nacos 配置中心 |

- **容器里不要用 `127.0.0.1`**：那指向容器自身。Compose 用服务名（`mysql` / `nacos` / `redis`），
  K8s 用 Service DNS。
- **网关强依赖 Nacos**：`gateway` 必须能通过 `registry.address` 发现后端，否则起不来；
  该键是嵌套键，需在配置文件/ConfigMap 里改成集群内的 Nacos 地址。
- **生产关掉 `auto_migrate`**：改用 Atlas 版本化迁移，SQL 落在
  [`deploy/script/migrations/`](../script/)，详见 [docs/ent.md](../../docs/ent.md)。
- **雪花 `node_id` 必须每副本唯一**（`[0,1023]`）：扩容前为每个实例分配不同值，
  否则多副本可能生成重复 ID。

## 加你自己的服务

按 [AGENTS.md](../../AGENTS.md) 的“新增资源清单”孵化服务后，照抄本目录的清单模板：
Compose 里加一个 `build.args.SERVICE=app/<svc>/cmd/<svc>` 的 service 块；K8s 里复制
`user_center.yaml` 改名、改端口、改 `SERVICE` 构建参数即可。
