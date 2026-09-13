# Apple Account 会话续期资料

本文只记录本地下载的开源参考实现，不代表 Apple Account 会话具有长期不掉线保证。

## 固定版本

| 项目 | 固定 commit | 证据 |
| --- | --- | --- |
| q1953258942/iCloud-Privacy-Mail | `3a839c7a6fb1a2f33b6cc680f4be63ae224fd165` | `internal/app/server.go:2706-2826`、`internal/app/icloud_client.go:363-412`、`cmd/panel/main.go:54-56` |
| cquxl/iCloud-Alias-Hub | `0e5b48db6583e30289a0c8447d93efa1f44d051a` | `internal/app/server.go:2888-3020`、`internal/app/icloud_client.go:363-412`、`cmd/panel/main.go:59-61` |
| pyicloud | `622cd160d8a259db644c8b3f3c96f76795a12f74` | `pyicloud/base.py:67-94, 285-365, 467-537` |

固定源码地址：

- <https://github.com/q1953258942/iCloud-Privacy-Mail/blob/3a839c7a6fb1a2f33b6cc680f4be63ae224fd165/internal/app/server.go>
- <https://github.com/cquxl/iCloud-Alias-Hub/blob/0e5b48db6583e30289a0c8447d93efa1f44d051a/internal/app/server.go>
- <https://github.com/picklepete/pyicloud/blob/622cd160d8a259db644c8b3f3c96f76795a12f74/pyicloud/base.py>

## Apple Account 管理态

两个参考仓库都实现了实际生产启动的后台 keepalive：面板启动入口调用 `StartAppleAccountKeepAlive`，后台循环立即执行一次，之后按 ticker 扫描已保存的 Apple Account 登录态。两者客户端 keepalive 都触碰管理 API，更新响应中的 Cookie、`scnt`、动态 API key 和 `timeOutInterval`；默认周期为 4 分钟，单次请求超时为 25 秒。

cquxl 版本在 `internal/app/server.go:2926-3002` 明确记录失败次数，并用 `keepAliveRetryDelay` 安排退避重试；成功后清零失败计数并保存新的状态。q 版本对应循环在 `internal/app/server.go:2744-2808`。

这两个仓库代码结构与实现同源，不能当作两份独立的长期稳定性实证；它们只能证明该续期策略在两个同源实现中存在生产调用路径。

## pyicloud 的不同协议

`pyicloud` 面向 iCloud Web services。启动时若有保存的 `session_token`，先调用 `setup/ws/1/validate`；失效后才重新登录（`base.py:285-345`）。每次请求从响应头保存 `session_id`、`session_token`、`trust_token`、`scnt`，并持久化 CookieJar（`base.py:67-94`）。

2FA 成功后它调用 `idmsa.apple.com/appleauth/auth/2sv/trust`，再以 `setup/ws/1/accountLogin`、`extended_login=True` 和 `trustToken` 换取 iCloud Web services 状态（`base.py:467-537`、`349-365`）。这是旧 iCloud Web 的 trust/token/Cookie 生命周期，不能与 Apple Account `account/manage` 管理态、动态 API key 或其 `timeOutInterval` 直接混用。
