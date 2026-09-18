# 邮箱池开发版

## 创建节奏（当前策略）

每个主号的自动创建计划使用本地预算：滚动 1 小时最多 18 次、至少间隔 2 分钟。手动批量创建和单次探测不使用本地额度，成功项之间不额外等待，也不修改自动计划或其 Apple 冷却。没有额外的每日 20 次限制；自动计划后端日额度为 432（18*24 的冗余约束），其失败和待确认尝试也计入。该保守上限是本项目策略，不是 Apple 官方配额保证。Apple 明确限流后自动计划和手动批量任务至少暂停 24 小时，不自动更换创建通道；`auto` 只选择本轮初始通道。自动计划的本地预算等待与 Apple 限流分别显示。批量任务生命周期 7 天。自动创建计划默认保持关闭。

本目录以 mangobubu/icloud-api 的 `32bd8ccc` 为基础开发，保留 Web 管理端、Apple 登录、自动创建、OTP API、只读 IMAPS、OAuth 和凭据轮换。

新增功能位于 Web 导航的「邮箱池」，包括邮箱库存、领取记录、项目 Key、库存目标和 API 接入说明。

## 首次配置

1. 在「主号管理」添加 iCloud 主号和 App 专用密码，完成 Apple 登录和 2FA，同步已有隐私邮箱。
2. 在「邮箱池 → 库存与定时创建」开启新邮箱入池，设置空闲目标并保存。目标 0 表示持续按原计划创建；正数表示达到目标后跳过远端创建。
3. 如需定时创建，由管理员主动开启。自动通道仅选择本轮初始创建通道，限流后不会自动切换；自动计划使用独立的本地额度，手动批量和单次探测直接请求 Apple。自动计划和手动批量任务收到 Apple 限流后至少暂停 24 小时，实际创建量以 Apple 返回为准。
4. 在「邮箱库存」选择已确认可分配的已有地址加入池。启用自动入池之前存在的地址保持原状；之后创建或首次同步进入本地的地址自动入池。
5. 在「项目 API Key」创建项目，保存一次性展示的 Key。每个项目的领取记录独立，所有项目共享管理员加入的空闲库存。

旧的 40 次/小时计划及其频率升级记录属于历史策略；新计划默认关闭，升级保留已有计划的开启/关闭状态。

## 手动创建 Apple 隐藏邮箱

在「主号管理 → 主号详情 → 批量创建隐私邮箱」填写数量 1-100（默认 5），选择自动、Apple Account 或 iCloud Web 旧通道后提交后台任务。任务会在页面关闭后继续运行；「停止后续创建」保留进行中的请求和部分进度。任务生命周期为 7 天；失败或待确认尝试同样计入额度，耗时不等于保证创建成功 100 个。
Apple 未登录时先完成登录，再点击创建。明确限流进入等待；网络错误或待确认结果则停止后续创建，先同步目录确认。

- 手动提交的批量任务不依赖自动开关、库存目标或本地创建额度；成功项之间不额外等待。「手动探测 1 个」是另一条单次入口，同样不使用本地额度，不受后台冷却阻挡，不改变后台冷却与计划；等待中的批量任务不会挡住探测。
- Apple 上游配额、主号启用状态、邮箱容量和结果确认仍生效。手动与自动共用账号锁。
- `auto` 只选择本轮初始通道，不因限流切换；明确选择的通道也不会自动改变。Apple Account 需要独立登录和 2FA，且仍需旧 Web 登录以同步地址归属。
- 手动批量收到 Apple 明确限流后至少暂停 24 小时；自动计划的本地预算等待是独立状态。任务生命周期 7 天。重启后 interrupted 任务不会自动重发，需人工确认。单次探测仅反馈本次结果，不自动重试，沿用 Apple 本次返回的 Retry-After 而非后台 24 小时等待。
- 成功后从隐私邮箱列表复制完整凭据。若启用自动入池，新邮箱按原入池流程进入库存。
- 关闭页面不会停止任务；需点击「停止后续创建」。结果不明时先刷新/同步目录，避免重复提交。

后台任务接口（管理员 session Cookie + `X-CSRF-Token`）：

- `POST /admin/api/v1/accounts/ACCOUNT_ID/aliases/creation-job`，JSON `{"count":5,"channel":"auto"}`，返回 `202 {"data":{"job":JOB}}`。
- `GET` 同路径读取最新任务；没有任务时 `job=null`。
- `POST` 同路径加 `/stop`，JSON `{}`，停止后续请求；进行中的请求保存结果后进入终态。
- `GET/POST/DELETE /admin/api/v1/accounts/ACCOUNT_ID/apple-account-auth` 管理新通道会话；`POST .../verify` 提交 challenge_id 和六位 code。
- 管理会话每 4 分钟尝试保活；会话失效后需重新登录，不自动保存或重放 Apple 密码。

单次探测接口：`POST /admin/api/v1/accounts/ACCOUNT_ID/aliases/create-now`，JSON `{"channel":"auto"}`；兼容旧 JSON `{}`，默认自动选择初始通道。
使用管理员 session Cookie 和 `X-CSRF-Token`，不是邮箱池项目 Key。
成功返回 `201 {"data":{"alias":ALIAS_DTO}}`，包含完整凭据且 `Cache-Control: no-store`。
同主号并发手动请求返回 `409 ALIAS_CREATION_BUSY`；Apple 限流返回 `429 APPLE_RATE_LIMITED`，可用时附带 `Retry-After`。

## 领取和使用

外部邮箱池接口统一使用 `Authorization: Bearer PROJECT_KEY`，PROJECT_KEY 以 `pool_` 开头。

```sh
curl 'https://mail.example.com/api/v1/pool/claim' \
  -H 'Authorization: Bearer PROJECT_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"request_id":"job-20260912-0001","count":1,"ttl_seconds":1800}'
```

可选 `account_id`、`group_id` 限定主号或分组。`count` 默认 1，范围 1–50；`ttl_seconds` 默认 1800，范围 60–86400。`request_id` 必填，8–128 个字母、数字、横线或下划线。

领取前持久化 request_id 和原参数。相同项目、相同编号和相同参数重试会返回原领取记录；参数变化返回 `409 IDEMPOTENCY_CONFLICT`。库存不足返回 `409 POOL_EMPTY`，整批保持未分配状态。领取完成后重试旧编号仍返回原记录，只有仍在领取期或已经确认使用的记录会带邮箱凭据。

## 已使用邮箱退休删除

供项目侧完成业务后回收**已使用**隐藏邮箱。此接口只删除指定项目已领取并已使用的隐藏邮箱；不删除 iCloud 主号，也不扫描或删除该主号的其他地址。它与旧的管理端 `DELETE /admin/api/v1/aliases/batch` 独立，旧接口的同步与异步语义不变。

提交和读取都需要双重鉴权：有效管理员 Session，以及该项目的 `Authorization: Bearer pool_...`。提交还必须提供同源 `X-CSRF-Token`；读取 GET 不要求 CSRF。路径使用实际管理前缀：`<admin-path>/api/v1/pool/aliases/batch`。

```sh
curl -X POST 'https://mail.example.com/ADMIN_PATH/api/v1/pool/aliases/batch' \
  -H 'Cookie: icloud_admin_session=ADMIN_SESSION' \
  -H 'X-CSRF-Token: ADMIN_CSRF' \
  -H 'Authorization: Bearer pool_PROJECT_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"operation_id":"retire-20260918-0001","items":[{"alias_id":123,"lease_id":"LEASE_ID"}]}'
```

- `operation_id` 为 16–128 位 ASCII 字母、数字、`-`、`_`；`items` 1–100 项，顺序属于幂等内容的一部分。
- 服务先整批预检：lease 必须精确对应 alias、属于当前 Pool Key 项目、为该 alias 唯一 live lease 且状态为 `used`；alias 必须属于启用、已认证的 iCloud 主号，不能是目录确认中地址。任一项目失败则整批不接收，所有 lease 保持原状。
- 接收时单事务将 lease 置为 `retiring`。此后取码、续期、释放、再次确认使用和再次领取均关闭。
- Apple 明确确认删除后，本地同一事务将 lease 置为 `retired`、`alias_id` 置空，并删除 alias 与 Pool 成员；历史 lease 保留。Apple 明确失败则恢复 `used` 并保留 alias。
- 网络结果未知、任务中断、服务重启或本地收口失败时，lease 保持 `retiring`，item 标为 `review`，任务为 `review` 或 `interrupted`；服务不自动重放。Apple 执行沿用现有每主号串行、批量并发和限流恢复策略。
- `operation_id` 在安装内全局唯一。原项目以完全相同、顺序一致的 items 重试时返回原任务，不重复删除；内容不同或被其他项目占用时返回 `409 IDEMPOTENCY_CONFLICT`，且不泄露原作业。客户端首次提交前必须保存 operation_id 与原 items。

读取任务：

```sh
curl 'https://mail.example.com/ADMIN_PATH/api/v1/pool/aliases/batch/jobs/retire-20260918-0001' \
  -H 'Cookie: icloud_admin_session=ADMIN_SESSION' \
  -H 'Authorization: Bearer pool_PROJECT_KEY'
```

响应仅含任务状态及逐项 alias/lease 结果，不返回项目 Key、邮箱 API Key、IMAP/OAuth 凭据、Cookie 或邮件内容：

```json
{
  "data": {
    "operation_id": "retire-20260918-0001",
    "status": "completed",
    "total": 1,
    "processed": 1,
    "items": [{"alias_id": 123, "lease_id": "LEASE_ID", "state": "retired", "result_code": ""}]
  }
}
```

成功响应形状：

```json
{
  "data": {
    "leases": [{
      "lease": {
        "id": "LEASE_ID",
        "alias_id": 1,
        "address": "alias@icloud.com",
        "project": "my-app",
        "request_id": "job-20260912-0001",
        "state": "leased",
        "created_at": "2026-09-12T08:00:00Z",
        "expires_at": "2026-09-12T08:30:00Z"
      },
      "mailbox": {
        "email": "alias@icloud.com",
        "api_key": "MAILBOX_API_KEY",
        "imap_password": "IMAP_PASSWORD",
        "client_id": "CLIENT_ID",
        "refresh_token": "REFRESH_TOKEN",
        "credential_version": 2,
        "otp_path": "/api/v1/otp?token=DERIVED_TOKEN",
        "code_path": "/api/v1/pool/leases/LEASE_ID/code",
        "oauth_token_path": "/oauth2/v2.0/token",
        "imap_host": "imap.example.com",
        "imap_port": 1993,
        "imap_tls": true
      }
    }]
  }
}
```

响应路径相对于当前服务域名。IMAPS/OAuth 凭据由本服务签发；客户端连接本服务的 IMAPS。部署时将 `ICLOUD_API_PUBLIC_IMAP_SERVER_NAME` 设为实际访问名称；当前外部 IMAPS 端口为 1993。

| 请求 | 用途 |
| --- | --- |
| `GET /api/v1/pool/leases` | 当前项目的领取记录，支持 state、limit、offset |
| `GET /api/v1/pool/leases/LEASE_ID` | 取回领取记录及仍有效的完整邮箱凭据 |
| `GET /api/v1/pool/leases/LEASE_ID/code` | 查询领取时间之后的最新六位验证码 |
| `POST /api/v1/pool/leases/LEASE_ID/commit` | 确认使用，持续保留邮箱分配 |
| `POST /api/v1/pool/leases/LEASE_ID/release` | 释放尚未确认使用的邮箱，并轮换邮箱凭据 |
| `POST /api/v1/pool/leases/LEASE_ID/renew` | JSON `{"ttl_seconds":1800}`，从当前时间续期 |

`code` 可追加 `after=RFC3339`，查询起点取 after 与领取时间中较晚者；`data.success=true` 时读取 `data.otp` 和 `data.time`，否则 `data.code=no_code`、`data.retryable=true`。请求会触发该 alias 的按需读取；与该邮箱的 OTP、Bearer 和直达链接共用限流，请求处理中及完成后 3 秒内返回 `429 RATE_LIMITED`、`Retry-After: 3`。每主号最多容纳 2 个取件请求，全站最多同时处理 16 个、每秒接纳 100 次，超量不排队。限制按服务进程计数。同主号最短 30 秒 fetch guard 可能增加等待时间。每次最多一批 128 封目标邮件，无后台续跑。首次读取最近 4096 个 UID 数值窗口，后续按该 alias 独立游标继续。客户端应等上次响应结束后至少 3 秒再请求，429 时遵循 `Retry-After`。

## 状态与凭据

- 空闲 → 已领取 → 已使用。已使用邮箱持续分配，过期回收只处理尚未确认的领取。
- 释放或到期回收后，邮箱回到空闲；领取及回收均在事务中轮换 API Key、IMAP 密码、client_id 和 refresh_token，旧访问令牌随凭据版本失效。
- 后台每 15 秒检查到期记录，每轮最多 200 条。过期的领取接口立即停止返回凭据和验证码；已经发出的邮箱凭据在回收事务提交时失效。
- 重新入池只忽略已经入池的记录，不重置已领取或已使用状态。暂停分配只作用于空闲邮箱。
- 停用主号时，下属邮箱从隐藏邮箱列表和邮箱池入池候选中隐藏，邮箱池成员也不会进入可分配库存；系统保留停用前的启用及池成员快照。重新启用主号后恢复邮箱列表显示，并按该快照恢复：原本启用且在池内的邮箱恢复可分配，原本单独停用或不在池内的邮箱维持原状态。此过程只暂停/恢复有效状态，不删除邮箱、凭据、邮件或领取历史。
- 邮件历史随地址保留，完整 IMAPS 与原 OTP API 仍可读取该地址的既有归档；本次任务取码使用 lease code 接口。
- 项目 Key 轮换保留其领取记录。停用项目会停止邮箱池 API 访问，独立邮箱凭据仍按自身状态生效，可在「隐私邮箱」页面轮换或停用。
- PostgreSQL 和 SQLite 均使用事务及唯一索引防止重复分配；本部署运行单应用实例，保留上游调度和全量轮换的进程内协调要求。

## 部署与维护

```sh
# 在解压的项目目录中，首次安装先复制环境示例。
cp -n .env.example .env
docker compose up -d --build --wait
docker compose ps
curl -fsS http://127.0.0.1:8788/healthz
```

新安装使用自己的 Web/API 域名，例如 `mail.example.com`。HTTPS 反向代理转发到 `127.0.0.1:8788`；公网启用 HTTPS Cookie。公开 IMAPS 使用独立域名及 TLS 证书，具体见 [公网部署](PUBLIC.md)。

获取管理入口和管理员密码：

```sh
docker compose exec -T icloud-api cat /app/keys/admin-path
docker compose exec -T icloud-api cat /app/keys/admin-password
```

浏览器打开自己部署的 `https://mail.example.com/admin/`，用户名 `admin`。环境示例设置 `ICLOUD_API_ADMIN_PATH=/admin`，密码由首次启动独立生成。未配置公网域名时可使用 [交付说明](../DELIVERY.md) 中的 SSH 转发访问。

数据库、keys 和 mail archive 共同组成恢复点，备份沿用主 README 的成组流程。新增 `pool_*` 表随 PostgreSQL 备份完整保存。开发分支为 `feature/mailbox-pool`，更新上游时保留此分支的本地修改并重新构建。

可运行 `sh scripts/backup-local.sh`，会短暂停止应用完成一致性备份，随后自动启动。输出位于 `.local/backups/UTC时间/`，包含 PostgreSQL、密钥、邮件正文、部署环境，以及存在时的公网 IMAPS 证书与校验文件。项目 Key 在自己安装的控制面板中创建和轮换。

验证命令：

```sh
go test ./...
go vet ./...
go test -race ./internal/store ./internal/httpserver ./internal/autocreate -run '^TestPool'
cd web
npm ci
npm test
npm run build
```
