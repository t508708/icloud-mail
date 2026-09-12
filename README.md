# iCloud 隐私邮箱归档 API v2

本地开发版新增 Web 邮箱池：项目 Key、批量自动取号、幂等领取、确认/释放/续期、凭据联动轮换和按库存定时创建。使用说明见 [邮箱池与本机部署](docs/POOL.md)。本机 HTTP 端口为 `8788`，其余上游说明中的 `8080` 按需替换。

本服务把 iCloud 隐私邮箱的新邮件归档到本地，并且只向外提供两种取件能力：

1. `GET /api/v1/otp`：按邮箱返回持续累积的验证码历史。
2. 标准只读 IMAPS：按邮箱读取完整 MIME；正文已淘汰或不可用时返回保留原标题的占位邮件。

`POST /oauth2/v2.0/token` 只为 IMAPS 的 XOAUTH2 登录签发一小时访问令牌，不构成第三种取件方式。服务签发的 API Key、IMAP 密码、client ID、refresh token 和 access token 都是本服务凭据，不是 Microsoft 凭据。

> [!WARNING]
> 本项目是非官方自托管工具，依赖 iCloud IMAP 和可能变化的 Apple Web 接口。上线前请使用可承受风险的账号验证登录、隐私邮箱目录同步和真实投递，并把 PostgreSQL、keys 卷与 mail archive 卷纳入同一个备份恢复点。本服务适合作为取码和邮件归档入口，不应成为重要邮件的唯一副本。

## 核心能力

- 管理多个 iCloud 主号及其隐私邮箱，继续支持目录同步和自动创建。
- 升级后归档同步游标之后的全部新 UID，包括上游已读和未读邮件。
- 新建隐私邮箱拥有独立、版本化的 API Key、IMAP 密码、client ID 和 refresh token；迁移前已经领取的旧 alias 保留 legacy API Key 和直达链接。
- 通过派生取码 URL 或 Bearer API Key 重复读取最近 100 条验证码。
- 通过密码或 XOAUTH2 登录只读 IMAPS，读取完整 MIME、稳定本地 UID 和归档占位邮件。
- 原始 MIME 按 SHA-256 去重保存到独立卷，容量超限时只淘汰最早正文，永久保留邮件元数据。
- 管理界面和首选管理 API 使用首次启动生成的随机路径；固定 `/admin/api/v1` 仅作为旧客户端兼容 API 入口。

```text
iCloud 主号 INBOX
  └─ 增量同步与 MIME 去重
      ├─ 隐私邮箱 A ─ OTP API / 只读 IMAPS
      ├─ 隐私邮箱 B ─ OTP API / 只读 IMAPS
      └─ 隐私邮箱 C ─ OTP API / 只读 IMAPS
```

## 快速启动

要求 Docker Engine、Docker Compose v2。直连 iCloud IMAP 时需要 iCloud 主号 App 专用密码；iCloud 转发第三方 IMAP 或自定义邮箱模式则填写对应邮箱服务的 IMAP 密码。

```bash
docker compose up -d --build --wait
docker compose ps
curl -fsS http://127.0.0.1:8080/healthz
```

HTTP 默认只发布在 `127.0.0.1:8080`，IMAPS 默认只发布在 `127.0.0.1:1993`。首次启动会在 keys 卷中生成管理员密码、随机管理路径、外部登记接口的 OAuth token、主密钥，以及本地持久化 IMAPS 自签证书。

```bash
docker compose exec -T icloud-api cat /app/keys/admin-password
docker compose exec -T icloud-api cat /app/keys/admin-path
docker compose exec -T icloud-api cat /app/keys/oauth-token
docker compose exec -T icloud-api cat /app/keys/public-imap-cert.pem
```

`admin-path` 的值形如 `/<32位小写十六进制>/admin/`。管理界面、静态资源和前端路由跟随这个随机前缀，管理 API 的首选入口是去掉该值结尾 `/` 后再拼接 `/api/v1`。OpenAPI 的 `{admin_path}` 变量则使用去掉首尾 `/` 的值。为兼容升级前客户端，同一套 JSON 管理 API 也保留在固定 `/admin/api/v1`；固定 `/admin` 不提供管理界面。登录成功会为两个 API 路径签发同一会话的受限 Cookie，两个入口都执行相同的登录限流、会话认证和 CSRF 校验。管理响应默认使用 `Cache-Control: no-store, private`，返回明文凭证的处理器会覆盖为 `no-store`。随机路径只能降低未授权扫描噪声，不应被当作访问控制边界。

使用 `admin` 和首次生成的密码登录随机管理路径，添加 iCloud 主号后同步或手动登记隐私邮箱。管理端支持创建邮箱分组，并在“全部隐私邮箱”或主号详情中把单个、勾选的隐私邮箱移动到所选分组；删除分组不会删除邮箱，只会将其恢复为未分组。对已完成 Apple 登录且主号没有同步错误的 iCloud 隐私邮箱，可以在“全部隐私邮箱”中勾选后执行“从 Apple 删除”：服务会先调用 Apple 的停用/永久删除流程，只有 Apple 确认删除成功后才清理本地记录；明确失败的项目会保留对应本地记录并返回逐项错误。批量操作支持后台任务及状态轮询；任务中断后尚无结果的项目，其远端结果待核查，不承诺本地记录仍在，详见下方“批量 Apple 删除后台任务”。自定义邮箱不走此 Apple 删除流程。公开接口说明位于 <http://127.0.0.1:8080/docs/>，机器可读契约见 [`docs/openapi.yaml`](docs/openapi.yaml)。

每个主号可配置上游隐式 TLS IMAP 主机、端口和登录用户名，默认是 `imap.mail.me.com:993`。已有隐私邮箱后仍可修改这三项，但这代表切换邮箱来源：服务会清除该主号旧来源的同步游标、v1 快照、消费与 `Seen` 状态、v2 归档和 OTP 历史，轮换公开 IMAPS 的 `UIDVALIDITY`，再从新来源建立不回填历史的基线。单纯修改 IMAP 密码（直连 iCloud 使用 App 专用密码，第三方转发使用第三方 IMAP 密码）或重新启用主号只重置同步状态，不删除已有邮件。已有隐私邮箱后主号邮箱地址仍不可修改。

收件规则分为三套：

1. `mailbox_type=icloud` + 默认 `imap.mail.me.com:993` + iCloud 主号用户名：直连 iCloud IMAP，按 Apple 原始投递头匹配隐私邮箱。
2. `mailbox_type=icloud` + 非默认 IMAP 来源（主机/端口或用户名不同于 iCloud 默认）：iCloud 隐私邮箱经转发后从第三方 IMAP 读取；仍保留 iCloud 类型，按 `X-ICLOUD-HME` 和转发链路投递头匹配。
3. `mailbox_type=custom`：自定义域名邮箱，按 `X-Original-To`、`Original-Recipient`、`Delivered-To` 等原始投递头匹配本地地址。

如果 iCloud 隐私邮箱已在 iCloud 设置中转发到第三方邮箱，`邮箱类型` 仍应选择“iCloud 隐私邮箱”，而 IMAP 主机和用户名应填写第三方邮箱的 IMAP 来源；用户名应使用最终物理投递目标/第三方 IMAP 登录邮箱（例如 `mango@example.com`）。服务会用 `X-ICLOUD-HME` 的 `p=`/`f=` 路由信息，并结合 `Delivered-To`、`X-Original-To`、`Original-Recipient` 以及常见 Apple 原始收件人头识别隐私邮箱。Apple 的 `f=` 可能是中间转发地址，不一定等于最终 `Delivered-To`，所以原始投递头中应同时能核对转发链和配置的最终目标。若转发服务删掉 `X-ICLOUD-HME` 和包含隐私地址的原始投递头、只在 `To/Cc` 留下隐私地址，默认安全策略不会信任该地址；仅可在理解伪造风险后显式启用弱收件人头。不要因为 IMAP 来源是第三方就把主号改成“自定义邮箱”，那会停用 iCloud 的地址同步和自动创建规则。

以上三套规则只决定邮件取件与归属。Apple 隐私邮箱目录同步和自动创建仍按 Apple 返回的默认转发目标与 iCloud 主号邮箱做现有校验；如果 Apple 的默认转发目标是另一个中间地址（例如标头中的 `f=`），现有地址可以正常取件，但目录同步或自动创建可能报告转发目标不匹配。完整支持该场景需要单独保存 Apple 转发目标，不能用最终 IMAP 用户名代替中间地址。

添加主号时也可以选择“自定义邮箱”。自定义模式单独保存邮箱后缀（例如 `example.com`），同一后缀只能配置一个主号；IMAP 密码使用 `imap_password` 提交并按原值加密保存。它不会调用 Apple，也不会改变 iCloud 隐私邮箱原有的每小时自动创建规则。在主号详情中输入生成数量即可批量生成随机地址，格式为 8–12 位小写英文字母和数字加 `@后缀`，同一批次和全局地址表都会阻止重复，地址也不能与主号的 IMAP 登录身份相同。单次最多生成 1000 个，可多次分批生成，`custom` 主号的累计数量不设上限。自定义地址的删除只清理本地记录，不会请求 Apple。

## 批量 Apple 删除后台任务

以下路径均相对于 `<admin-path>/api/v1`，固定兼容入口为 `/admin/api/v1`。提交和轮询都需要有效管理 Session；`DELETE` 还需 `X-CSRF-Token` 及同源校验。后台任务只脱离提交请求的连接生命周期，不免除管理会话和凭据轮换保护。

| 请求 | 成功响应 | 用途 |
| --- | --- | --- |
| `DELETE /aliases/batch`，正文 `{"alias_ids":[101,102]}` | `200 data {requested,deleted,failed,results}` | 保留原同步接口及结果格式 |
| `DELETE /aliases/batch?async=1`，正文含 `alias_ids` 和 `operation_id` | `202 data {job_id,status,requested,processed,deleted,failed,results,request_id,created_at,updated_at}` | 提交后台任务，或取得幂等重试对应的原任务 |
| `GET /aliases/batch/jobs/latest` | `200 data` 为任务对象或 `null` | 优先返回当前管理员的 active 任务，否则返回按创建时间最近的任务；没有任务时为 `null` |
| `GET /aliases/batch/jobs/:jobID` | `200 data` 为任务对象 | 按 ID 轮询；ID 格式无效或当前管理员名下无该任务时返回 `404`，不泄露其他管理员的同 ID 任务 |

异步请求示例正文：

```json
{
  "alias_ids": [101, 102],
  "operation_id": "a1b2c3d4-5678-4abc-8def-0123456789ab"
}
```

`alias_ids` 为 1–1000 个不重复的正整数。异步模式必填 `operation_id`：16–128 个 ASCII 字母、数字、`-` 或 `_`。`job_id` 就是原 `operation_id`，任务身份和幂等键按管理员隔离；不同管理员可以使用相同 `operation_id` 创建各自的任务，读取只返回当前管理员名下的任务。客户端仍建议使用 UUID，并在首次提交前保存此键和原始 ID 列表（包括顺序）。

若连接中断或响应丢失，优先直接 `GET /aliases/batch/jobs/<原 operation_id>`，即使尚未收到提交响应也可定位同一任务；页面恢复且未保存键时可查询 `jobs/latest`。同一管理员以相同 `operation_id` 和完全相同、顺序一致的 `alias_ids` 重试会返回原任务，包括并发重复提交，不重复删除；服务先查幂等键，再对新任务预检邮箱，因此原任务已经删除的本地 ID 不会使此类重试失败。原任务已 `completed` 或 `interrupted` 时也不会重新执行。不要因超时生成新键或重排、删减原列表。

任务状态为 `queued`、`running`、`completed`、`interrupted`，其中前两者属于 active。`completed` 表示本次处理已经结束，不表示所有地址删除成功；应结合 `deleted`、`failed` 和逐项结果判断。`processed` 只统计已持久化完成结果的项目，`requested` 表示请求总数；`request_id` 保留首次提交的追踪标识，与后续查询响应的 `X-Request-ID` 不必相同，`created_at`、`updated_at` 为 UTC RFC 3339 时间字符串（可含小数秒）。

正常进度的 `results` 只包含已有完成结果的项目，按原 ID 列表顺序返回，不为待处理项预填成功或失败；初始可为空数组，此时 `processed=deleted=failed=0`。每项包含 `id`、`address`、`deleted`，错误项可带 `code`、`message`、`local_retained`。`interrupted` 是例外：HTTP 响应会为尚无完成结果的剩余项合成 `code: BATCH_DELETE_INTERRUPTED`，说明“任务已中断，远端删除结果待核查”。这些项计入 `failed`，不计入 `processed`；其 `deleted: false` 不是远端仍存在的证明，`local_retained` 在实现中为 false、在 JSON 中省略，也不证明本地已经删除。不要把它们当作已完成项，也不要仅凭 `results.length` 或 `deleted+failed` 计算已处理进度。确认远端状态后再决定后续操作。

任务进度与对应逐项 audit 在同一事务中持久化；停止服务会取消后台执行并等待有界收尾，重启将遗留未终结任务标为 `interrupted`，不自动重放。已确认结果保留；本地任务/审计事务不等于 Apple 远端与数据库之间的原子事务，因此中断时仍可能存在待核查项目。后台运行上限为 2 小时；该上限不是完成全部删除的时长承诺。

每个管理员最多 1 个 active 任务，服务全局最多 2 个。原任务的幂等重试不新占任务名额；其他提交及全量轮换按以下规则处理：

| 条件 | HTTP / 错误码 | 调用方处理 |
| --- | --- | --- |
| 提交新任务时，当前管理员已有 active 任务或凭据正在轮换 | `409 BATCH_DELETE_IN_PROGRESS` | 查看当前管理员任务；轮换后重新登录 |
| 提交新任务时，服务全局任务名额已满 | `429 BATCH_DELETE_BUSY` | 稍后重试同一操作 |
| 同一管理员的相同 `operation_id` 携带不同 ID 列表（含顺序变化） | `409 IDEMPOTENCY_CONFLICT` | 核对原请求，保留原操作的键与列表绑定 |
| 后台任务执行器尚未启动或正在关闭 | `503 BATCH_DELETE_UNAVAILABLE` | 等服务就绪后重试 |
| 存在 active 删除任务时请求全量凭据轮换 | `409 BATCH_DELETE_IN_PROGRESS` | 等任务结束后再轮换；轮换不会占锁等待任务，从而阻塞进度轮询 |

Apple 调用按账号组织：同一账号逐条处理，复用会话校验（validate）和目录（directory）结果，并沿用每次响应更新后的会话；每个任务内不同账号最多 2 路并发，这与全局最多 2 个 active 任务是两层限制。异常时会重新读取并验证远端状态，不会仅清理本地记录来跳过 Apple 确认；若新一次读取的 Apple 目录确认地址已不存在，可以清理对应本地记录，旧缓存中的缺失本身不构成确认。在同一账号、无异常且 N 个地址都需要先停用再删除的模拟请求计数中，复用可将 `4N` 次调用降到 `2N+2`；这只是请求次数示例，不是实际耗时或固定加速比。实际时长和成功数仍取决于 Apple 响应、限流、会话状态及异常复核，单次接收 1000 项不代表承诺 1000 项成功。

后台删除对同一主号的请求设置至少 1 秒间隔；这是客户端的保守节流策略，不是 Apple 公布的配额。首次校验、读取目录或删除步骤出现限流时，任务保持 `running`，按 60、120、240 秒退避等待；若 Apple 提供更长的 `Retry-After`，采用更长等待。每个主号在同一批次最多恢复 3 次，成功读取或切换邮箱不会重置预算。等待结束后先用最新目录核对状态，再决定是否继续停用或删除，避免盲目重发已经成功的操作。会话失效、身份不匹配和持久化失败不按限流重试；停止服务及任务的 2 小时运行时限仍有效。

等待期间响应可带 `waits` 数组，含 `account_id`、`alias_id`、`operation`、`retry_at`、`attempt`、`max_attempts`，以及可用时的 `http_status`、`service_code`。等待不是终态，不增加 `processed` 或 `failed`；这些信息是当前进程的诊断状态，页面刷新后仍可查询，重启后按原规则显示 `interrupted`，不恢复倒计时或重放删除。持续限流用尽恢复次数后，实际受限项返回 `APPLE_RATE_LIMITED`，同主号后续未尝试项返回 `APPLE_BATCH_DEFERRED`，并保留本地记录；可选 `deferred` 计数是 `failed` 的子集，不应再次相加。界面仅重试状态查询，不会因为限流另建删除任务。日志保留固定步骤名、状态码及受约束的业务码，不记录上游响应正文或登录凭据。

后台删除对同一主号的请求设置至少 1 秒间隔；这是客户端的保守节流策略，不是 Apple 公布的配额。首次校验、读取目录或删除步骤出现限流时，任务保持 `running`，按 60、120、240 秒退避等待；若 Apple 提供更长的 `Retry-After`，采用更长等待。每个主号在同一批次最多恢复 3 次，成功读取或切换邮箱不会重置预算。等待结束后先用最新目录核对状态，再决定是否继续停用或删除，避免盲目重发已经成功的操作。会话失效、身份不匹配和持久化失败不按限流重试；停止服务及任务的 2 小时运行时限仍有效。

等待期间响应可带 `waits` 数组，含 `account_id`、`alias_id`、`operation`、`retry_at`、`attempt`、`max_attempts`，以及可用时的 `http_status`、`service_code`。等待不是终态，不增加 `processed` 或 `failed`；这些信息是当前进程的诊断状态，页面刷新后仍可查询，重启后按原规则显示 `interrupted`，不恢复倒计时或重放删除。持续限流用尽恢复次数后，实际受限项返回 `APPLE_RATE_LIMITED`，同主号后续未尝试项返回 `APPLE_BATCH_DEFERRED`，并保留本地记录；可选 `deferred` 计数是 `failed` 的子集，不应再次相加。界面仅重试状态查询，不会因为限流另建删除任务。日志保留固定步骤名、状态码及受约束的业务码，不记录上游响应正文或登录凭据。

限流等待只暂停对应主号：等待中的主号会让出任务内的执行名额，其他未限流主号继续处理，即使多个主号同时冷却也不会挡住后续正常主号。每个任务仍最多 2 个主号实际执行 Apple 操作；`waits` 可以与其他主号持续增长的 `processed`、`deleted` 同时出现。冷却等待也不会占用主号设置锁，期间仍可开启或关闭“自动创建隐私邮箱”；切换开关只更新本地计划，不会跳过 Apple 冷却。同一主号的实际 Apple 操作仍串行执行，批量删除恢复前会重新获取执行名额及账号锁，并检查身份及隐私邮箱状态。

## 管理端凭证与复制格式

新建或已领取 v2 凭证的隐私邮箱会显示完整的 API Key、IMAP 密码、client ID 和 refresh token。迁移前已经领取的 legacy alias 只显示已有 Key 前缀和旧直达链接，不会伪造不存在的 v2 凭证。管理端支持单条、勾选批量和全部复制；输出一行一个邮箱、无表头。

取码链接严格使用五个横线：

```text
alias@example.com-----https://HOST/api/v1/otp?token=DERIVED_TOKEN
```

IMAP/OAuth 严格使用四个横线：

```text
alias@example.com----IMAP_PASSWORD----CLIENT_ID----REFRESH_TOKEN
```

管理 API 的凭证响应设置 `Cache-Control: no-store`。legacy alias 使用兼容接口只轮换 API Key 和由其派生的旧直达链接，保留消费记录、邮件快照、IMAP `Seen` 状态及 credential mode。v2 alias 可显式轮换整套凭证，使旧 API Key、派生取码 URL、IMAP 密码、refresh token 和所有已签发 access token 同时失效。schema 迁移本身不会轮换已领取的 legacy Key。

需要在凭据泄露后整体止损时，可在安装级随机管理前缀或固定兼容入口调用 `POST <admin-path>/api/v1/aliases/rotate-all-credentials`。该接口要求有效管理 Session、`X-CSRF-Token` 和当前管理员密码，并提交 JSON `{"confirmation":"ROTATE_ALL","current_password":"CURRENT_ADMIN_PASSWORD"}`。重认证具有独立限流；`current_password` 与 `confirmation` 只用于本次校验，不得写入访问日志、应用日志或审计详情。

服务存在 `queued` 或 `running` 的批量 Apple 删除任务时，全量轮换返回 `409 BATCH_DELETE_IN_PROGRESS`，请等待任务结束后重试；此时不执行轮换，也不持有轮换锁等待后台删除，以保持任务进度可轮询。

全量轮换在同一事务中把所有 legacy alias 强制迁移为 v2，并为所有现有 v2 alias 轮换整套凭据。等待 Apple 目录确认的 alias 也会轮换；对应 `pending_alias_api_keys` 会同步更新为新凭证包的 API Key，原确认关系和领取流程保持不变。响应的 `data` 只返回 `total`、`rotated`、`migrated_legacy`、`rotated_v2`、`rotated_pending` 和固定为 `true` 的 `reauthentication_required`，不返回任何新凭据；其中 `rotated_pending` 是已轮换总数的子集。执行后，旧 legacy API Key 与 v1 直达链接，以及原 v2 API Key、派生链接、IMAP/OAuth 凭据和 access token 全部失效；邮件归档、消费/已读状态和 IMAP `Seen` 任务保留。管理员密码本身不变，但服务会提升执行管理员的 `password_version`、撤销该管理员的全部后台 Session，并清除随机管理路径与固定兼容路径的 Cookie，必须重新登录。

轮换与所有会消费或返回 alias 凭据的公开 HTTP 请求形成明确截止点。`GET /api/v1/otp`、`GET /api/v1/mail/latest`、`GET /api/v1/mail/recent[/]`、`POST /oauth2/v2.0/token` 和外部 `POST /api/v1/aliases[/]` 从鉴权前一直保护到响应写完；全量轮换会先等待这些已在途请求完成，再提交并返回成功。成功响应之后，使用任何旧 HTTP 凭据发起的新请求都会被拒绝。

IMAPS 不等待 HTTP 截止点。轮换提交后，旧 IMAP 密码和旧 access token 立即不能建立新会话；已经登录的会话会在下一条邮箱命令复验凭据版本并拒绝继续操作，IDLE 会话最迟在下一次轮询 tick 失效。提交时已经通过复验并开始执行的单条命令可以完成。

## OTP API

可以使用完整 API Key 的 Bearer 鉴权：

```bash
curl -H 'Authorization: Bearer API_KEY' \
  'https://HOST/api/v1/otp'
```

也可以直接访问管理端复制的派生取码 URL：

```bash
curl 'https://HOST/api/v1/otp?token=DERIVED_TOKEN'
```

默认成功响应是裸 JSON 数组，最新优先，每个邮箱最多保留 100 个非空 OTP 值：

```json
[{"otp":"876543","time":"2026-08-11T12:00:00+08:00"},{"otp":"123456","time":"2026-08-11T11:00:00+08:00"}]
```

设置 `ICLOUD_API_OTP_RETURN_LATEST_ONLY=true` 后，有记录时只返回上述数组第一项的裸 JSON 对象，也就是真正最新的有效 OTP：

```json
{"otp":"876543","time":"2026-08-11T12:00:00+08:00"}
```

旧变量 `ICLOUD_API_OTP_RETURN_LAST_ONLY` 继续作为兼容别名，开启后同样返回最新 OTP；同时配置两个变量时，以 `ICLOUD_API_OTP_RETURN_LATEST_ONLY` 为准。两种模式在没有验证码时都返回 `200 []`。重复请求不会消费验证码、不会改变本地状态，也不会给上游邮件设置已读标志。迁移和轮换后 OTP URL 仍使用 `/api/v1/otp`；v2 alias 的 OTP token 保持用途隔离的 v3 公开信封版本，与 recent-mail 的 v2 信封分别签名，不能通过改写 URL 路径或信封版本互换。这里的 v3 仅指 OTP token 信封版本，不是新的 alias credential mode。原 v1 直达 token 只继续用于尚未执行全量迁移的 legacy alias 的 `/api/v1/mail/recent`。OTP 从主题、纯文本和 HTML 可读文本中提取，只接受未与字母或数字相邻的 6 位 ASCII 数字；每封邮件最多保存一个候选，升级前保存的非 6 位历史值也会在读取时忽略。

## 旧接口兼容

PR2 保留原有 v1 外部接口，已有调用方无需改 URL 或鉴权方式：

| 接口 | 鉴权 | 兼容语义 |
| --- | --- | --- |
| `GET /api/v1/mail/latest` | `Authorization: Bearer <API Key>` | 可重复读取当前最新完整快照，不消费邮件、不改变已读状态 |
| `GET /api/v1/mail/recent` | `?api_key=<API Key、legacy 旧直达 token 或 v2 recent-mail token>` | 返回最近一小时内当前快照；成功后记录消费并异步写入 IMAP `Seen` |
| `POST /api/v1/aliases` | `Authorization: Bearer <OAuth Token>` | 使用 query、表单或二者组合传递既有 `add_hide_my_eamil` 和 `icloud` 字段；合并后每个字段必须恰好出现一次 |

默认兼容升级中，旧 alias 的 API Key、旧直达链接和消费状态继续有效。迁移前已经领取的 alias 保持 `legacy` 模式；新建 alias，以及迁移前存在 pending 一次性 Key 但尚未领取的 alias，才会补齐 v2 凭证。显式调用全量轮换接口是例外：所有 legacy alias 会迁移为 v2，旧 API Key 与 v1 直达链接立即失效；`pending_alias_api_keys` 行不会删除，而会在同一事务中替换为新凭证包的 API Key。`latest_messages`、`consumed_messages`、`imap_seen_tasks` 和 pending 确认流程会保留并继续参与兼容流程。

外部登记接口仍读取 `ICLOUD_API_OAUTH_TOKEN`（或等价的部署密钥注入），升级过程不应删除已配置的 OAuth Token。成功响应继续使用 `api_key` 和 `mail_api_direct_link` 字段；管理 API 的 alias DTO 继续提供 `direct_link_path`，新字段可作为附加信息但不能替代旧字段。

```bash
curl --fail-with-body -H 'Authorization: Bearer API_KEY' \
  'https://HOST/api/v1/mail/latest'

curl --fail-with-body \
  'https://HOST/api/v1/mail/recent?api_key=API_KEY_OR_DIRECT_TOKEN'

curl --fail-with-body -X POST \
  -H "Authorization: Bearer ${ICLOUD_API_OAUTH_TOKEN}" \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'add_hide_my_eamil=alias@icloud.com' \
  --data-urlencode 'icloud=primary@icloud.com' \
  'https://HOST/api/v1/aliases'
```

自动创建的 pending Key 仍通过管理 API 的 `GET` `<admin-path>/api/v1/accounts/{id}/aliases/auto-create/keys` 领取，并用同一路径的 `DELETE` 请求、提交 `alias_ids` 确认保存；读取不会删除队列，确认操作才会删除对应行。管理端兼容接口 `POST <admin-path>/api/v1/aliases/{id}/rotate-key` 只轮换 API Key；`POST <admin-path>/api/v1/aliases/{id}/rotate-credentials` 只用于 v2 alias 的整套凭证轮换。两者都只在显式调用后使对应旧凭据失效。上述 `<admin-path>/api/v1` 均可替换为固定兼容入口 `/admin/api/v1`。

## IMAPS 与令牌接口

IMAPS 用户名固定为隐私邮箱地址。可使用管理端显示的 IMAP 密码登录，也可先交换 access token，再使用 XOAUTH2 登录。

```bash
curl -X POST 'https://HOST/oauth2/v2.0/token' \
  -H 'Content-Type: application/x-www-form-urlencoded' \
  --data-urlencode 'grant_type=refresh_token' \
  --data-urlencode 'client_id=CLIENT_ID' \
  --data-urlencode 'refresh_token=REFRESH_TOKEN'
```

响应采用标准 Bearer 字段：

```json
{
  "access_token": "ACCESS_TOKEN",
  "token_type": "Bearer",
  "expires_in": 3600
}
```

服务只提供一个只读 `INBOX`，支持 `LIST`、`STATUS`、`SELECT`、`EXAMINE`、`SEARCH`、`FETCH`、`UID FETCH` 和 `IDLE`。`APPEND`、`STORE`、`COPY`、`MOVE`、`DELETE`、`EXPUNGE` 等写操作返回 `READ-ONLY` 错误。

未显式配置生产证书时，服务会在 keys 卷中生成覆盖 `ICLOUD_API_PUBLIC_IMAP_SERVER_NAME` 的持久化自签证书。客户端必须信任该证书。生产环境应同时配置：

```text
ICLOUD_API_PUBLIC_IMAP_TLS_CERT_FILE=/path/to/fullchain.pem
ICLOUD_API_PUBLIC_IMAP_TLS_KEY_FILE=/path/to/private-key.pem
ICLOUD_API_PUBLIC_IMAP_SERVER_NAME=imap.example.com
```

证书的 SAN 必须覆盖 `ICLOUD_API_PUBLIC_IMAP_SERVER_NAME`。

## 生产部署

Compose 默认把 HTTP 和 IMAPS 都绑定在宿主机回环地址。对外服务时：

1. 用 HTTPS 反向代理转发 `127.0.0.1:8080`，保留外部 `Host`、`Origin` 和协议。
2. 设置 `ICLOUD_API_COOKIE_SECURE=true`，并把 `ICLOUD_API_TRUSTED_PROXIES` 收紧为实际代理地址或网段。
3. 通过防火墙受控开放 IMAPS；可把宿主机 `993` 映射到容器 `1993`，也可使用支持 TLS 透传的 TCP 代理。
4. 将生产证书和私钥通过只读 bind mount、Compose secret 或受保护的 keys 卷提供给容器，再设置证书路径与服务器名称。

Nginx 的 HTTP 反向代理至少保留：

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $http_host;
    proxy_set_header Origin $http_origin;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

管理端复制的取码 URL 根据当前请求的外部主机名和协议生成，所以代理必须保留 `Host` 并正确传递 `X-Forwarded-Proto`。生产 IMAPS 应使用受客户端信任的证书；本地生成的自签证书适合回环和测试环境。

## 从 Outlook / Graph 客户端迁移

只需要验证码的调用方应直接改用管理端导出的五横线取码 URL；原先使用 Graph 获取完整邮件的调用方可迁移到只读 IMAPS。已经具备 Outlook IMAP OAuth2 分支的客户端通常只需：

- 把令牌地址改为本服务的 `/oauth2/v2.0/token`。
- 表单只发送 `grant_type`、`client_id`、`refresh_token`，移除 Microsoft `scope`。
- 把 IMAPS 主机改为部署方地址，用户名改为对应隐私邮箱。
- 使用本服务导出的 IMAP 密码，或继续使用标准 XOAUTH2 Bearer 格式。
- 用 `UIDVALIDITY + UID` 保存读取游标；归档邮箱为只读模型，`STORE \Seen` 返回 `READ-ONLY`。

四横线导出顺序与常见 Outlook 导入格式一致，但其中每项凭据都由本服务签发。Graph 文件夹、写操作和 deltaLink 逻辑应改为单一 `INBOX` 与本地稳定 UID 语义。

## 归档与留存

- 升级完成后只处理同步游标之后的新 UID；已读和未读邮件都会归档。
- 增量同步默认每批处理最多 32 个候选 UID；每批原子保存邮件和游标后自动续跑，减少大量正文逐封读取导致的整批超时重做。
- 升级不会回填远端历史。v1 的最新快照只迁移标题和时间元数据，并分配稳定的本地 UID 1。
- PostgreSQL 保存邮件元数据、SHA-256、alias 映射、本地稳定 UID 和 OTP；原始 MIME 保存在独立 `icloud_api_mail_archive` 卷。
- 同一上游邮件投递给多个 alias 时只保存一份 MIME，各 alias 拥有自己的稳定邮箱 UID。
- `ICLOUD_API_MAIL_CONTENT_LIMIT_BYTES` 默认 `10737418240`（10 GiB）。每批提交后按收件时间全局 FIFO 淘汰最早正文，标题、时间、发件人、Message-ID、本地 UID 和历史记录永久保留。
- `ICLOUD_API_MAX_MESSAGE_BYTES` 默认且最高为 `104857600`（100 MiB）。超限邮件只保存元数据；抓取完整邮件时先流式写入临时文件，不把大 MIME 整体读入内存。
- 缺失、损坏、超限或已淘汰的内容在 IMAPS 中表现为说明性占位邮件，并保留原标题。

主要归档配置：

| 环境变量 | 默认值 | 说明 |
| --- | ---: | --- |
| `ICLOUD_API_PORT` | `8080` | Compose 的宿主机 HTTP 回环端口 |
| `ICLOUD_API_IMAPS_PORT` | `1993` | Compose 的宿主机 IMAPS 回环端口 |
| `ICLOUD_API_MAIL_CONTENT_LIMIT_BYTES` | `10737418240` | 全局 MIME 内容容量 |
| `ICLOUD_API_MAX_MESSAGE_BYTES` | `104857600` | 单封邮件硬上限，最大 100 MiB |
| `ICLOUD_API_MAX_BODY_BYTES` | `524288` | OTP/MIME 元数据解析时的正文预算 |
| `ICLOUD_API_OTP_RETURN_LATEST_ONLY` | `false` | 有 OTP 时只返回最新一项对象；空历史仍返回 `[]` |
| `ICLOUD_API_OTP_RETURN_LAST_ONLY` | `false` | 旧配置兼容别名；未设置正式变量时同样启用最新单条模式 |
| `ICLOUD_API_PUBLIC_IMAP_ADDR` | `127.0.0.1:1993` | IMAPS 监听地址；Compose 中为 `0.0.0.0:1993` |
| `ICLOUD_API_PUBLIC_IMAP_SERVER_NAME` | `localhost` | IMAPS TLS 名称 |
| `ICLOUD_API_PUBLIC_IMAP_TLS_CERT_FILE` | 空（自动生成） | 生产证书在容器中的路径；证书和私钥需同时配置 |
| `ICLOUD_API_PUBLIC_IMAP_TLS_KEY_FILE` | 空（自动生成） | 生产私钥在容器中的路径 |
| `ICLOUD_API_POLL_INTERVAL` | `10s` | 自动同步周期 |
| `ICLOUD_API_IMAP_TIMEOUT` | `8s` | 单次上游 IMAP 操作时限 |
| `ICLOUD_API_SYNC_TIMEOUT` | `70s` | 单个账号同步时限 |
| `ICLOUD_API_SYNC_CONCURRENCY` | `3` | 同步并发数，范围 1–16 |
| `ICLOUD_API_SESSION_TTL` | `8h` | 管理端会话有效期 |
| `ICLOUD_API_COOKIE_SECURE` | `false` | HTTPS 生产部署应设为 `true` |
| `ICLOUD_API_TRUSTED_PROXIES` | 私有网段 | 受信反向代理 IP/CIDR，生产环境应收紧 |
| `ICLOUD_API_ALLOW_WEAK_RECIPIENT_HEADERS` | `false` | 是否接受弱收件人头；保持默认值 |
| `TZ` | `Asia/Shanghai` | OTP API 输出时间所用时区 |

## v1 → v2 兼容升级

schema 升级后，服务会先校验主密钥，再按 alias ID 逐条事务补齐 v2 凭证包。迁移前已经领取的 alias 保持 `legacy` 模式，原 API Key、旧直达链接、消费记录和 IMAP `Seen` 任务不变；只有新 alias，或迁移前存在 pending 一次性 Key 但尚未领取的 alias，才会使用 v2 凭证。pending Key 会在补齐 v2 凭证时复用原 Key，不会静默替换。

升级会保留 `latest_messages`、`consumed_messages`、`imap_seen_tasks`、`pending_alias_api_keys` 及 `api_key_prefix`。归档表与旧快照双写，旧邮件路由继续读取兼容快照；安装级 OAuth Token 配置也继续可用于 `/api/v1/aliases`。如果曾部署过 PR2 的前序 v7 版本，应从管理端重新复制 v2 的 OTP 与 recent-mail URL，以换用用途隔离 token；迁移前 legacy alias 已保存的 v1 直达链接默认不受 schema 升级影响。只有管理员显式调用全量轮换接口时，legacy alias 才会强制迁移为 v2 并使旧链接失效；等待 Apple 确认的项也会同步轮换 pending Key，同时保留既有确认流程。升级从既有游标建立归档基线，远端历史邮件不会回填。

升级前必须同时备份 PostgreSQL、keys 卷和 mail archive 卷。不要只备份数据库：keys 卷包含主密钥、随机管理路径和本地 IMAPS 证书，mail archive 卷包含数据库所引用的 MIME 文件。使用卷快照时还应把 `postgres_data`、`postgres_config`、`installation_state`、`icloud_api_keys` 和 `icloud_api_mail_archive` 作为同一个恢复点；`postgres_socket` 属于临时通信卷。

以下逻辑备份流程先停止应用，避免归档文件与数据库在备份期间继续变化，PostgreSQL 保持运行以完成 `pg_dump`：

```bash
docker compose stop icloud-api

docker compose exec -T postgres \
  /usr/local/bin/icloud-api-postgres-entrypoint backup > postgres.dump

docker compose run --rm --no-deps \
  --entrypoint /usr/local/bin/icloud-api-keys-maintenance \
  icloud-api backup > keys.tar

docker compose run --rm --no-deps --entrypoint tar \
  icloud-api -C /app/mail-archive -cf - . > mail-archive.tar

docker compose start icloud-api
```

恢复或回滚时应停止 HTTP、IMAPS 和同步器，在同一个维护窗口中恢复三份匹配的备份。keys 维护器只接受已知文件，并校验管理路径以及本地 IMAPS 证书/私钥必须成对存在。回滚到 v1 代码时必须同时恢复升级前数据库，不能让 v1 直接读取 v2 schema。

不要执行 `docker compose down -v`。主密钥丢失后，Apple 会话与每个 alias 的完整凭证包都不能解密；mail archive 卷与数据库不匹配时，IMAPS 会将缺失内容降级为占位邮件。

升级完成后执行：

```bash
docker compose up -d --build --wait
docker compose ps
curl -fsS http://127.0.0.1:8080/healthz
docker compose exec -T icloud-api cat /app/keys/admin-path
```

然后验证旧 `/api/v1/mail/latest`、`/api/v1/mail/recent` 和 `/api/v1/aliases` 调用仍可用，已有 legacy Key 和直达链接仍能读取/消费邮件；再为新 alias 分发 OTP 或 IMAP/OAuth 凭据，并验证新邮件能够通过两套接口读取。

## 本地开发验证

本仓库使用 Go 1.26、Node.js 22。常用命令：

```bash
go test ./...
go vet ./...
go build ./cmd/icloud-api

cd web
npm ci
npm test
npm run build
```

集成回归应使用隔离的 iCloud 测试账号和真实 Apple IMAP 端点；生产进程不会接受测试 IMAP 地址、TLS 名称或 CA 覆盖。

## 安全与运行注意事项

- 生产环境保持单个 `icloud-api` 实例，避免拆分进程内账号锁、同步调度和限流状态。
- 管理端、部署级 OAuth token、API Key、取码 URL、IMAP 密码、refresh token、主密钥、App 专用密码和备份都按敏感凭据管理。
- 全量轮换请求的 `current_password` 和 `confirmation` 不得进入反向代理访问日志、应用日志或审计详情；不要记录管理 API 请求体。
- URL 可能进入浏览器历史、代理日志和监控系统；legacy alias 可轮换 API Key 使旧直达链接失效，v2 alias 应轮换整套凭证。
- 原始 MIME、主题和 HTML 都属于外部输入；展示 HTML 前应清理内容或放入严格隔离的沙箱。
- 邮件归属依赖 iCloud 转发链路中的收件人头。使用第三方转发邮箱时，应把 IMAP 用户名填写为最终物理投递/登录地址，以便在 Apple 原始收件人头被改写后仍能校验目标；保持 `ICLOUD_API_ALLOW_WEAK_RECIPIENT_HEADERS=false`，并用真实 Hide My Email 样本验收路由。
- 在边缘代理设置请求速率、连接数和正文大小限制，限制管理路径的来源网络，并定期演练 PostgreSQL、keys 与 mail archive 的成组恢复。
