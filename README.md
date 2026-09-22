# Webhook 投递服务

一个自托管的出站 Webhook 投递服务：使用方登记回调 URL、HMAC 密钥和订阅的事件类型；事件进来后异步、至少一次地投递出去，支持按顺序键串行、失败退避重试、死信搁置与手动重投。

- 后端：Go 1.25+（仅依赖 `pgx` 驱动 + PostgreSQL，无消息队列中间件）
- 前端：Vue 3 + Vite（构建产物由 Go 二进制 `embed`，单镜像部署）
- 依赖：PostgreSQL，由 `docker compose` 拉起
- 测试：Go 集成测试跑在真实 PostgreSQL 上（内嵌 PostgreSQL 自动启动，也可用外部库）

## 快速开始

```bash
docker compose up --build
```

- 打开 http://localhost:8080 —— Vue 管理界面
- API 基址：`http://localhost:8080/api`
- PostgreSQL：`localhost:5432`（webhook/webhook）
- 示例回调接收器：http://localhost:9000 （同一 compose 网络内主机名为 `receiver`，密钥 `demo-secret-please-change`）

表结构在服务启动时自动迁移（`backend/internal/db/schema.sql`，幂等）。

### 三步自测（UI）

1. **回调地址 → 登记回调**
   - URL 填 `http://receiver:9000/hook`（从浏览器外部自测可用 `http://localhost:9000/hook`，但容器内投递要用服务名 `receiver`）
   - 密钥填 `demo-secret-please-change`
   - 订阅事件填 `invoice.paid`
2. **事件入口 → 发布事件**
   - 类型 `invoice.paid`，顺序键可填 `customer-42`，payload 随意 JSON
   - 连续发两条相同顺序键的事件，去「投递历史」观察二者严格按序成功
3. **失败路径**：把回调 URL 改成带故障注入的地址（见下），观察重试、死信、再在「死信队列」点「重投」

> 浏览器访问容器内的 `receiver` 主机名不通是正常的：`receiver` 只在 compose 网络内可解析。若从宿主直接登记，URL 用 `http://localhost:9000`（端口已映射）。

### 示例接收器的故障注入

接收器会校验签名和重放，并支持在 URL 上注入故障：

| URL 查询参数 | 行为 |
| --- | --- |
| `?mode=fail500` | 始终返回 500（触发退避重试） |
| `?mode=fail404` | 始终返回 404（永久失败，直接进死信） |
| `?mode=flaky` | 同一投递前两次 502，第三次 200 |
| `?mode=timeout` | 挂住连接 30s（触发单次尝试超时） |
| `?delay=2s` | 固定延迟 |

接收器也可单独运行：`server receiver -addr :9000 -secret <密钥> -window 5m`。

## 运行测试

方式一，Docker（推荐，与 CI 一致）：

```bash
docker compose up -d postgres
docker compose run --rm tests
```

方式二，本机（不装 PostgreSQL 也行，测试会自动下载并启动内嵌 PostgreSQL 16）：

```bash
cd backend
go test ./...
```

指定外部 PostgreSQL 时，每个测试会用独立 schema 隔离：

```bash
TEST_DATABASE_URL='postgres://webhook:webhook@localhost:5432/webhook?sslmode=disable' go test ./...
```

测试覆盖（对应需求）：

| 需求 | 测试 |
| --- | --- |
| 有序投递（同 key 不乱序、队头可见、不同 key 不互相阻塞） | `TestOrderedDelivery_SameKeyFIFO`、`TestOrderedDelivery_HeadOfLineBlocking` |
| 失败重试 + 退避 | `TestRetry_RecoversAfter5xx` |
| 重试用尽进死信 | `TestRetry_ExhaustsIntoDeadLetter` |
| 死信手动重投 | 同上（redrive 后清零再投成功） |
| 4xx 永久失败不重试 | `TestRetry_Permanent4xxGoesStraightToDead` |
| 超时 / 租约接管 / 不允许双成功 | `TestLeaseTakeoverAfterTimeout`、`TestLeaseTakeover_LateCommitRejected` |
| 签名校验 + 时间窗 + nonce 防重放 | `internal/signing/signing_test.go` |
| 5xx / 429 / 4xx / 网络断开 / 超时分类 | `internal/deliver/sender_test.go` |

前端本地开发（热更新，API 代理到 8080）：

```bash
cd frontend && npm install && npm run dev
```

## HTTP API 摘要

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| `POST` | `/api/endpoints` | 登记回调 `{url, secret, events[], active?, description?}` |
| `GET/PUT/DELETE` | `/api/endpoints/{id}` | 查询 / 更新 / 删除 |
| `POST` | `/api/events` | 发布事件 `{type, payload, order_key?}`（payload ≤ 1MiB） |
| `GET` | `/api/events` | 最近事件 |
| `GET` | `/api/deliveries?status=&endpoint_id=` | 投递历史（含队列位置、阻塞原因） |
| `GET` | `/api/deliveries/{id}` | 投递详情（持有者、租约到期、下次可投、错误） |
| `GET` | `/api/deliveries/{id}/attempts` | 每次尝试的明细 |
| `POST` | `/api/deliveries/{id}/redrive` | 死信手动重投（仅 `dead` 状态可用，尝试次数清零） |
| `GET` | `/api/stats` | 各状态计数 |

## 投递语义与并发约定（重要）

**状态机**

```
pending ──认领(lease)──▶ in_flight ──2xx────────────▶ succeeded
   ▲                        │
   │                        ├── 可重试失败(超时/5xx/429/网络) ──退避到期──▶ pending
   │                        └── 永久失败(其他4xx) / 超过 max_attempts ──▶ dead
   └──────────── redrive（手动）───────────────────────────────────── dead
```

**认领（claim）**：worker 周期性扫描，在一个事务里用
`SELECT ... FOR UPDATE SKIP LOCKED` 锁定到期候选行，再原子更新为 `in_flight`
并写入租约（`leased_by` = worker 标识，`leased_until` = now + 租约时长）。
多个 worker 抢同一任务时，只有一个能拿到行锁，因此同一条投递不会被两个进程同时认领。

**有序投递**：事件携带 `order_key` 时，认领 SQL 对每条候选检查同一
`(endpoint_id, order_key)` 下是否存在更早创建、且尚未成功的投递；有则跳过。
效果是：

- 同一回调 + 同一 key 严格 FIFO，不允许乱序并行成功；
- 队头没成功，后面的可以一直堵在队头（`queue_position` 与
  `blocked_reason=head_of_line` 在列表接口和界面上可见）；
- 队头若进了死信，同 key 的后续投递同样会被挡住——严格 FIFO 下不允许跳过
  失败的前序事件；在死信队列手动重投（成功，或处理好业务后删除回调重建）
  即可解锁；
- 不同 key、以及不带 key 的投递互不阻塞。

**谁在投 / 超时谁接管**：每条 in-flight 投递都记录 `leased_by`、
`leased_at`、`leased_until`（界面「详情」可见）。worker 每轮先执行租约回收：
`leased_until < now()` 的 in-flight 投递被标记为一次 `timeout` 尝试、回到
`pending`，任何 worker 都能立刻重新认领——这就是接管（takeover）。
默认租约 45s，单次 HTTP 超时 20s（租约 > 超时，正常慢响应不会误接管）。

**不允许“两边都显示成功”**：终态提交（成功/失败）的 SQL 都带条件
`WHERE id=? AND status='in_flight' AND leased_by=<自己>`。旧 worker 若在
接管之后才返回，更新行数为 0：它的那次尝试只能记为 `lost_lease`，
无法覆盖接管者写入的结果。测试 `TestLeaseTakeover_LateCommitRejected`
固化了这一保证。代价是语义为**至少一次**：接管瞬间旧请求可能其实已到达
对方，因此极端情况下会发出两次 HTTP；接收方应按
`X-Webhook-Event-Id`（+ `X-Webhook-Delivery-Id`、`X-Webhook-Attempt`）幂等去重。

**重试策略**：指数退避 + 抖动，`delay = min(backoff_max, backoff_base * 2^(n-1))`
再在 `[0.5x, 1x)` 区间抖动，默认 `2s → 4s → 8s → ...`，上限 10 分钟。
默认最多 6 次尝试；对 `429/503` 会尊重 `Retry-After`（上限 1 小时）。
4xx（429 除外）视为永久失败，不重试、直接进死信。

**失败原因分类**（界面和 API 中分开显示）：`timeout`（超时）、
`http_5xx`（服务端错误，可重试）、`http_4xx`（客户端错误，不重试）、
`network`（连接失败/DNS 等）、`lease_timeout`（租约过期被接管）、
`lease_lost`（旧 worker 的迟到结果）。

## 签名与防重放

每次回调请求（`POST`，JSON body 结构见下）携带：

| Header | 内容 |
| --- | --- |
| `X-Webhook-Signature` | `t=<unix秒>,v1=<hex(HMAC-SHA256(secret, t + "." + rawBody))>` |
| `X-Webhook-Timestamp` | 与签名中相同的 unix 秒 |
| `X-Webhook-Nonce` | 每次尝试新生成的 16 字节随机十六进制串 |
| `X-Webhook-Event-Id` / `X-Webhook-Delivery-Id` / `X-Webhook-Attempt` | 幂等标识 |

接收方校验步骤（示例接收器 `backend/internal/demo` 就是参考实现）：

1. 用相同密钥对 `<timestamp>.<原始请求体字节>` 计算 HMAC-SHA256，恒定时间比较 `v1`；
2. 时间戳与当前时间偏差超过窗口（默认 5 分钟）则拒绝——挡旧请求重放；
3. 记录并拒绝窗口内重复的 `X-Webhook-Nonce`——一次性随机串，双保险。

回调 body：

```json
{
  "id": "42",
  "type": "invoice.paid",
  "data": { "...": "发布事件时的 payload" },
  "order_key": "customer-42",
  "created_at": "2026-09-21T10:00:00+08:00"
}
```

## 配置（环境变量）

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `HTTP_ADDR` | `:8080` | 监听地址 |
| `DATABASE_URL` | `postgres://webhook:webhook@localhost:5432/webhook?sslmode=disable` | PostgreSQL DSN |
| `WORKER_ID` | `<主机名>-<pid>` | 租约持有者标识 |
| `WORKER_CONCURRENCY` | `4` | 单进程认领后并发执行 HTTP 的槽位数 |
| `POLL_INTERVAL` | `500ms` | 扫描间隔 |
| `ATTEMPT_TIMEOUT` | `20s` | 单次 HTTP 超时 |
| `LEASE_DURATION` | `45s` | 认领租约时长（须 > 单次超时） |
| `BACKOFF_BASE` / `BACKOFF_MAX` | `2s` / `10m` | 退避起点与上限 |
| `DEFAULT_MAX_ATTEMPTS` | `6` | 每条投递最大尝试次数 |
| `SIGNATURE_WINDOW` | `5m` | 签名时间窗 / nonce 保留时长 |

扩容方式：直接起多个指向同一个 PostgreSQL 的进程（Deployment 多副本即可），
无需选主；认领与终态提交的行锁/条件更新保证了跨进程互斥。

## 目录结构

```
backend/
  cmd/server/            入口（API + worker 同进程；receiver 子命令）；web/dist 为内嵌前端
  internal/db/           SQL：迁移、认领(FIFO+SKIP LOCKED)、租约回收、终态提交、死信重投
  internal/deliver/      HTTP 执行器：签名头、超时/状态码分类、Retry-After、退避
  internal/signing/      HMAC 签名/验签、时间窗、nonce 接口
  internal/worker/       投递循环、接管、提交守卫
  internal/apiserver/    REST API + SPA 托管
  internal/demo/         可验签/防重放/注入故障的示例接收器
  internal/testsupport/  测试数据库（内嵌 PG 或外部库 + 独立 schema）
frontend/                Vue 3 界面（投递历史 / 死信 / 回调 / 事件）
```
