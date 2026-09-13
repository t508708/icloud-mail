# Apple Account 会话续期

本文记录开源依据、运行证据和本项目修复；会话有效性由 Apple 的实际响应决定。

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

## 运行证据与修复

2026-09-13 的运行日志记录两个启用主号在自动创建时收到 HTTP 401，随后计划因 `APPLE_ACCOUNT_SESSION_EXPIRED` 停止。重新登录后的管理态有效期约为 15 分钟。修复前只有创建时到期刷新的调用；`KeepAliveAccountSession` 没有接入生产后台循环。

- 服务启动一个可取消的续期 worker，每分钟只检查本地记录；15 分钟 TTL 通常在到期前三分钟续期，较短 TTL 按其五分之一提前。缺失 TTL 时用四分钟兜底间隔。只处理启用的 iCloud 主号和已保存的管理态，与邮箱创建额度独立。
- 续期依次读取管理 token、API key 和 `forwardemail` 管理资源，保存 Apple 实际返回的 TTL、Cookie、SCNT；不重投密码或 2FA，也不读取邮件或创建地址。未复制参考项目的 JS 遥测上报。
- 瞬时故障持久化 2、4、8、16、30 分钟退避，并遵守更长的 Retry-After。明确会话失效后停止后台重试，新的成功登录恢复续期。停用主号不发续期请求。
- Cookie 轮换使用管理态自己的 `UpdatedAt` 排序，避免较晚保存的旧 Web 快照覆盖已续期的 ACC。多步刷新中，前面已接受的 Cookie/SCNT 轮换也会在后续步骤失败时保留。
- 日志区分“Apple Account 会话已续期”和“Apple Account 会话续期暂未完成”，自动创建日志保留具体管理端点名称。按需邮件接收和每小时 18 次后台创建额度保持独立。

## 验证

离线测试覆盖提前续期、管理资源访问、轮换状态传递、失败退避持久化、旧 Web 快照覆盖、会话拒绝后停止、停用主号跳过以及 worker 取消。Apple、HME、调度和启动包测试通过，Apple、HME 和启动包 race 检查通过。

首轮上线观察：两个主号在 21:55 自动续期成功，原约 21:57 的过期时间延长至约 22:10，原登录时间未变化；随后自动创建任务成功完成，其中一个主号在原过期时间之后的 21:59 仍成功创建。最终部署重启后，两个主号仍为 authenticated，已续期的有效期保持。此记录证明本次现有登录态续期成功，不代表 Apple 永久免验证承诺。
