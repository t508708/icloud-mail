# Apple Account 风险调研（2026-09-13）

本文件是公开实现的行为研究，不复制大块代码，也不把参考项目的做法表述为账号健康或防停用保证。三个参考仓库均未提供通用许可证；`pyicloud` 与 `imapflow` 为 MIT，仅作接口与工程实践参考。

## 公开参考项目

### `mangobubu/icloud-api`

- 固定 HEAD [`32bd8cc`](https://github.com/mangobubu/icloud-api/tree/32bd8ccc967834af72243916b94d0f668ba33fed)。SRP 登录/2FA、session token exchange、429/5xx 的 Retry-After 元数据见 [`internal/apple/client.go`](https://github.com/mangobubu/icloud-api/blob/32bd8ccc967834af72243916b94d0f668ba33fed/internal/apple/client.go#L122-L155)。
- Cookie 有可导出的持久 Jar（[`cookies.go#L14-L89`](https://github.com/mangobubu/icloud-api/blob/32bd8ccc967834af72243916b94d0f668ba33fed/internal/apple/cookies.go#L14-L89)），Apple session 由 store 保存；这代表复用登录态，不代表账号健康。
- 自动创建计划有 30s 轮询，并在观察到 Apple reserve 限流后安排 61m 冷却（[`manager.go#L30-L44`](https://github.com/mangobubu/icloud-api/blob/32bd8ccc967834af72243916b94d0f668ba33fed/internal/autocreate/manager.go#L30-L44)）；创建操作显式按 non-retryable 处理，避免不确定结果重复创建。
- 没有防停用承诺证据；README 反而记录限流、延迟与中断状态。无根目录 LICENSE/COPYING/NOTICE。

### `q1953258942/iCloud-Privacy-Mail`

- 固定 HEAD [`3a839c7`](https://github.com/q1953258942/iCloud-Privacy-Mail/tree/3a839c7a6fb1a2f33b6cc680f4be63ae224fd165)。Apple SRP/2FA 与 10m 内存 pending 状态见 [`apple_auth_client.go#L143-L154`](https://github.com/q1953258942/iCloud-Privacy-Mail/blob/3a839c7a6fb1a2f33b6cc680f4be63ae224fd165/internal/app/apple_auth_client.go#L143-L154)。
- 保存 cookies、登录态及管理态；管理态支持按需 Refresh 与 KeepAlive，默认保活 4m（[`icloud_client.go#L354-L380`](https://github.com/q1953258942/iCloud-Privacy-Mail/blob/3a839c7a6fb1a2f33b6cc680f4be63ae224fd165/internal/app/icloud_client.go#L354-L380)）。短重试为瞬态网络错误最多 3 次、800ms/1.6s。
- 创建限流采用按 Apple Account/iCloud Web 通道分别 2m 冷却（[`server.go#L29`](https://github.com/q1953258942/iCloud-Privacy-Mail/blob/3a839c7a6fb1a2f33b6cc680f4be63ae224fd165/internal/app/server.go#L29)、[`#L3628-L3659`](https://github.com/q1953258942/iCloud-Privacy-Mail/blob/3a839c7a6fb1a2f33b6cc680f4be63ae224fd165/internal/app/server.go#L3628-L3659)）。
- 无防停用承诺证据；无根目录通用许可证。

### `cquxl/iCloud-Alias-Hub`

- 固定 HEAD [`0e5b48d`](https://github.com/cquxl/iCloud-Alias-Hub/tree/0e5b48db6583e30289a0c8447d93efa1f44d051a)，基于上游 Privacy-Mail，实际 Apple 登录/刷新/Cookie 结构基本同源。
- 管理态 Refresh/KeepAlive 默认 4m；创建限流按通道 2m（[`server.go#L29`](https://github.com/cquxl/iCloud-Alias-Hub/blob/0e5b48db6583e30289a0c8447d93efa1f44d051a/internal/app/server.go#L29)、[`#L3912-L3939`](https://github.com/cquxl/iCloud-Alias-Hub/blob/0e5b48db6583e30289a0c8447d93efa1f44d051a/internal/app/server.go#L3912-L3939)）。这比本项目更积极，但仍不等于账号健康。
- README/NOTICE 明确不绕过限流、接口成功率不保证；没有防停用承诺证据。
- `NOTICE.md` 明确未授予 MIT/Apache 等通用许可证（[`NOTICE.md#L10-L12`](https://github.com/cquxl/iCloud-Alias-Hub/blob/0e5b48db6583e30289a0c8447d93efa1f44d051a/NOTICE.md#L10-L12)）。

## 其他工程参考

- [`pyicloud`](https://github.com/picklepete/pyicloud/blob/622cd160d8a259db644c8b3f3c96f76795a12f74/pyicloud/base.py#L285-L396)：MIT；保存 cookie/session，已有 token 先 validate，失败后外层可能完整登录。其普通 421/450/500 请求最多原样重试一次，不等同于本项目的单次 token 交换修复；本项目不采纳自动重投密码。
- [`imapflow`](https://github.com/postalsys/imapflow/blob/58ef92c1a09fd1e1da1af3564e3a13777c150536/src/imap-flow.ts#L4757-L4801)：MIT；同连接 mailbox lock 排队、UID/changedSince 增量读取。认证失败抛错，断线后由调用方决定重连，不无限自动重试。可借鉴其连接生命周期而非为现有 Go 服务整体引入 Node 邮件栈。

## 当前项目事实与判断

- 后台自动与批量共享 `18/h`，手动 probe 独立 `25/h`、`2m`；理论合计为 `43/h`，并非全主号 `18/h`。自动日额度为 `432`（18*24）。每次新通道常规至少 `Validate + List + add + complete + List` 五个 Apple HTTP 请求；TTL refresh/确认传播会增加请求。
- 当前统一连接管理：新建默认 auto、新优先；目录保留 Web（q 同样目录固定 Web）。界面合并不等价于 Apple 会话合并。
- KeepAlive 当前无生产调用；IMAP 按 alias `10s` 去重，`cmd main` 生产账号 guard 为 30s；fetch 仍逐次 LOGIN，短时连接复用尚待优化。
- 已修复：validate 421 不直接视为 expired，并最多一次可信 token `accountLogin` 恢复（允许一次全球到中国区路由修正）；不复投密码/2FA/创建/删除。现有证据未证明此前 550 停用由本项目造成。
- 采纳方向：失败不覆盖管理态，按需刷新短 TTL；不照搬“限流后换通道”、2m 冷却或持续保活。

## 后续优先改进（未实施）

1. 统一总预算治理，probe 更谨慎并纳入共享预算审计。
2. IMAP 短连接复用与断路器，减少逐次 LOGIN。
3. 记录按操作、账号、通道聚合的请求指标，支持限流因果审查。
4. 使用目录快照减少全表读取与重复 Apple List 请求。
