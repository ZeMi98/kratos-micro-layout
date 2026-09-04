# 中间件部署（deploy/middleware）

本目录提供 kratos-micro-layout 本地/自托管所需的中间件栈，全部通过一份
Docker Compose 清单拉起：

```
deploy/middleware/
  docker-compose.middleware.yaml   MySQL + Redis + Nacos（默认）；Elasticsearch + Kibana（search profile，选配）
  .env.example                     所有可调项的样例（含密钥生成命令）；复制为 .env 后按需修改
  mysql-init/                      首次启动时自动执行的初始化脚本（*.sql / *.sh），空目录占位
```

清单里每个凭据/端口都写成 `${VAR:-default}`，**默认值与 `configs/*.yaml` 对齐**，
所以不放 `.env` 也能直接跑起来。要覆盖任何值，复制样例再改：

```bash
cp deploy/middleware/.env.example deploy/middleware/.env
```

> `.env` 已在 `.gitignore` 中忽略，**永远不要提交填好的 `.env`**。真实密钥放这里或
> 交给密钥管理服务。

## 一键起停

仓库根目录提供了 Make 目标（等价于下面的 `docker compose` 命令）：

```bash
make middleware-up      # 起 MySQL + Redis + Nacos
make middleware-down    # 停（手动加 -v 可连同数据卷一起删）
```

等价的直接命令：

```bash
docker compose -f deploy/middleware/docker-compose.middleware.yaml up -d
docker compose -f deploy/middleware/docker-compose.middleware.yaml down       # 保留数据
docker compose -f deploy/middleware/docker-compose.middleware.yaml down -v    # 连数据卷一起删
```

查看状态与日志：

```bash
docker compose -f deploy/middleware/docker-compose.middleware.yaml ps
docker compose -f deploy/middleware/docker-compose.middleware.yaml logs -f nacos
```

## 服务与端口

| 服务 | 默认端口 | 用途 | 备注 |
|---|---|---|---|
| MySQL | 3306 | 模板唯一带驱动的数据库 | 首次启动建库 `user_center`，ent 再建表（`auto_migrate`） |
| Redis | 6379 | 缓存/会话预留位 | `configs/user_center.yaml` 有 `data.redis`，暂无代码读取 |
| Nacos | 8848 / 9848 | 注册中心 + 配置中心 | 8848 是 HTTP/控制台，9848 是 Go SDK 用的 gRPC 通道，**两个都要放** |
| Elasticsearch | 9200 / 9300 | 日志/检索（选配） | `search` profile |
| Kibana | 5601 | ES 可视化（选配） | `search` profile |

默认凭据（本地开发）：MySQL `root` / `123456`，库 `user_center`；Nacos 控制台
`nacos` / `nacos`（未开启鉴权时）。

## 选配：Elasticsearch + Kibana

这两个服务放在 `search` profile 下，`make middleware-up` **不会**拉起它们（保持默认栈
轻量，模板当前也没有代码读 ES）。需要时：

```bash
# 1) ES 要求宿主机 vm.max_map_count >= 262144
sudo sysctl -w vm.max_map_count=262144      # Linux；Docker Desktop(mac/win) 默认已满足

# 2) 带 profile 启动
docker compose -f deploy/middleware/docker-compose.middleware.yaml --profile search up -d
```

启动后访问 Kibana `http://localhost:5601`，用 `elastic` / `.env` 里的 `ELASTIC_PASSWORD`
登录。ES 开启了 security 但关闭了 TLS（HTTP + basic auth），仅供本地；生产请在反向代理
处终止 TLS，并把 Kibana 切到最小权限的 `kibana_system` 账号（其密码需单独用
`POST /_security/user/kibana_system/_password` 重置，`ELASTIC_PASSWORD` 不会设置它）。

## 开启 Nacos 鉴权（可选）

默认 **不开启** 鉴权，这样 `configs/*.yaml` 里空的 `registry.username/password` 与网关
都能直接工作。要开启，三步缺一不可：

1. 在 `.env` 里设 `NACOS_AUTH_ENABLE=true`，并填三个密钥（生成命令见 `.env.example`）：
   ```bash
   openssl rand -base64 32   # -> NACOS_AUTH_TOKEN（Base64，解码后 >= 32 字节）
   openssl rand -hex 16      # -> NACOS_AUTH_IDENTITY_VALUE（KEY 自定义，如 serverIdentity）
   ```
2. 在每个服务配置 `configs/*.yaml` 的 `registry` 段填上账号密码（Nacos 默认 `nacos/nacos`，
   建议改掉）：
   ```yaml
   registry:
     address: 127.0.0.1:8848
     username: nacos
     password: nacos
   ```
3. 重启 Nacos 与业务服务。

> 只开服务端鉴权、不给客户端配账号，会导致注册/发现/配置拉取全部 401。

## 覆盖端口 / 使用业务库

- **端口冲突**：改 `.env` 里的 `MYSQL_PORT` / `REDIS_PORT` / `NACOS_PORT` 等（只改宿主机侧），
  再把服务配置指向新端口。
- **业务库/独立账号**：在 `.env` 设 `MYSQL_DATABASE` / `MYSQL_USER` / `MYSQL_PASSWORD`
  （样例里给了 `shop_biz` / `biz_user` 的示例）。由于服务的 DSN 用 `${DB_SOURCE:...}` 占位符，
  可通过环境变量 `KRATOS_DB_SOURCE` 整体覆盖，无需改代码：
  ```bash
  export KRATOS_DB_SOURCE='biz_user:Biz@Pass2026@tcp(127.0.0.1:3306)/shop_biz?parseTime=true&loc=Local'
  ```
  注意 kratos 的 env 源只剥离 `KRATOS_` 前缀、不做小写化或 `_`→`.` 映射，因此**嵌套键**
  （如 `registry.address`）无法直接用 env 覆盖，只能在配置文件里用 `${VAR:default}` 占位符或
  走 Nacos 配置中心。

## 初始化脚本

把 `*.sql` / `*.sh` 放进 `deploy/middleware/mysql-init/`，MySQL 容器**首次**启动（数据卷为空时）
会按文件名顺序执行；已有数据卷时不会重跑。

## 生产注意

这份清单面向本地开发/自托管，**不是生产清单**：没有 TLS 终止、没有备份、没有做安全加固，
资源上限（`cpus` / `mem_limit`）也只是本地兜底。生产环境请使用托管服务或经过评审的
清单，并按 [docs/ent.md](../../docs/ent.md) 用 Atlas 版本化迁移替代 `auto_migrate`。
