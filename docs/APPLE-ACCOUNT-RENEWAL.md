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

- 服务启动一个可取消的续期 worker，只处理启用的 iCloud 主号和已保存的管理态，与邮箱创建额度独立。初版每分钟检查一次本地记录、以 TTL 提前量触发；当前调度见下方 2026-09-15 复查。
- 续期依次读取管理 token、API key 和 `forwardemail` 管理资源，保存 Apple 实际返回的 TTL、Cookie、SCNT；不重投密码或 2FA，也不读取邮件或创建地址。未复制参考项目的 JS 遥测上报。
- 瞬时故障持久化 2、4、8、16、30 分钟退避，并遵守更长的 Retry-After。明确会话失效后停止后台重试，新的成功登录恢复续期。停用主号不发续期请求。
- Cookie 轮换使用管理态自己的 `UpdatedAt` 排序，避免较晚保存的旧 Web 快照覆盖已续期的 ACC。多步刷新中，前面已接受的 Cookie/SCNT 轮换也会在后续步骤失败时保留。
- 日志区分“Apple Account 会话已续期”和“Apple Account 会话续期暂未完成”，自动创建日志保留具体管理端点名称。按需邮件接收和每小时 18 次后台创建额度保持独立。

## 验证

离线测试覆盖提前续期、管理资源访问、轮换状态传递、失败退避持久化、旧 Web 快照覆盖、会话拒绝后停止、停用主号跳过以及 worker 取消。Apple、HME、调度和启动包测试通过，Apple、HME 和启动包 race 检查通过。

首轮上线观察：两个主号在 21:55 自动续期成功，原约 21:57 的过期时间延长至约 22:10，原登录时间未变化；随后自动创建任务成功完成，其中一个主号在原过期时间之后的 21:59 仍成功创建。最终部署重启后，两个主号仍为 authenticated，已续期的有效期保持。此记录证明本次现有登录态续期成功，不代表 Apple 永久免验证承诺。

## 2026-09-14 长时会话复查

两个主号在前一晚 21:42 登录后，后台一直续期成功，但次日 05:42 与 05:44 的初始创建请求收到 401。最后一次成功刷新分别在 05:39 和 05:36，当时保存的短 TTL 分别到 05:54 和 05:51。约 8 小时后的失效发生于短 TTL 内，说明首轮验证只覆盖了短期空闲续期；当前证据没有证明 Apple 存在固定 8 小时的官方管理态上限。

只读检查重新登录后的 Cookie 元数据：管理 token Cookie 的到期时间随刷新滚动；`myacinfo` 是会话 Cookie，信任设备 Cookie 约 30 天。没有发现公开的 8 小时 Cookie 到期字段；短 TTL 和 Cookie 到期信息均不足以证明长期管理态可用。

与固定版本参考客户端 `internal/app/icloud_client.go:424-473` 对照，补齐已失效管理态的恢复分支：正常刷新遇到会话失效后，先读取门户，再保留 Cookie、暂不携带旧 SCNT 取得管理 token；随后重新读取 API key 和管理资源。若这次仍明确失效，仅再尝试一次已接受的 SCNT 检查点。限流、网络错误与门户暂时故障直接返回并退避，不扩大重试。失败响应不会覆盖已接受的 Cookie/SCNT。

创建初始 POST 明确返回 401、尚未取得候选地址时，允许在一次成功续期后重试一次。超时、429、5xx、不确定响应以及取得候选地址后的完成步骤均不重发。这用于处理短 TTL 内提前失效；多步创建的结果确认规则保持不变。

新增离线回归覆盖无 SCNT 恢复、旧检查点兜底、限流停止、失效停止以及初始 401 的有限重试。该恢复路径的长期实效仍需下一次超过原失效时长的运行观察，未据短期成功宣称彻底解决长时过期。

## 2026-09-15 再次失效与保活调度修正

本轮观察记录：两个测试安装主号在 11:10、11:11 登录，19:08 的管理接口检查仍成功并返回约 15 分钟 TTL。其中一个主号在 19:12 的创建恢复流程收到 token 端点 HTTP 401；另一个在 19:21 的后台续期收到 HTTP 401。距离原登录约 8 小时。此前的无 SCNT 恢复没有使这轮会话持续有效，故仍不能把短 TTL 刷新当作完整登录态长期存续的证据。

原调度在 15 分钟 TTL 到期前三分钟才发请求，加上一分钟扫描间隔，实际保活约 13 分钟一次。参考项目默认每 4 分钟主动执行一次管理态检查，并非仅在 TTL 临近到期时执行。新登录态的 Cookie 元数据同时显示，`caw` 约 5 分钟到期，`caw-at`、`awat` 约 15 分钟到期，受信设备 Cookie 约 30 天；这些名称的具体服务器语义尚无确证。

本次把两次成功续期之间的计划间隔改为每次随机 4–6 分钟，并将抽样秒数持久化到 Apple Account 会话；重启不会重新抽样形成固定边界。若 Apple 返回更短 TTL，仍在该期限前按五分之一提前；本地扫描周期改为 30 秒，为调度留出余量。退避时间优先于正常保活周期，明确失效后保持停止，停用主号不发请求。没有增加密码重投、设备验证码请求或 JS 遥测请求。

成功日志记录原登录时间、登录时长、Apple 短 TTL 与下一次计划时间；失败记录具体上游操作和 HTTP/服务码，持久化检查点日志保留最后成功时间、退避与明确失效状态，不记录 Cookie、SCNT 或 API key 的值。

这一修改纠正了与参考实现不一致的保活间隔。是否能延长约 8 小时后的实际寿命，仍须以同一登录态跨过该时长后的管理接口成功响应验证；单次刷新、单元测试和设备信任 Cookie 均不能代替这项验证。旧 iCloud Web 的 `accountLogin(extended_login=true)` 只交换 WebServices 状态，不应直接挪作 Apple Account 管理态的长效刷新凭据。
