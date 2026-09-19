# 按需触发的邮件接收

## 选型

保留现有 Go + PostgreSQL + MIME 文件归档，不增加 Node.js、Redis 或另一个邮件平台。
参考的是成熟项目的协议与状态管理设计，并非整体部署或拷贝它们的实现：

- [ImapFlow](https://github.com/postalsys/imapflow)：MIT 许可，支持 IDLE、邮箱锁和流式读取，以及按服务器能力启用的 CONDSTORE/QRESYNC 等扩展。
- [go-imap](https://github.com/emersion/go-imap)：本项目已使用的 Go IMAP 协议库；IDLE 命令自动约每 28 分钟续订，避免长期空闲超时。
- [EmailEngine](https://github.com/postalsys/emailengine)：参考通知唤醒与持久化同步状态的分工。其当前许可为商业源码可用许可，项目没有将它当作免费开源依赖引入。

默认 `ICLOUD_API_MAIL_ON_DEMAND_ONLY=true` 且启用 `ICLOUD_API_IMAP_IDLE_ENABLED`：服务按活跃主号建立共享 receiver，不在启动时执行全量收件，也不做无条件周期轮询。主号连续 10 分钟没有有效取件后 receiver 休眠；每个主号最多保持 1 条 IDLE 通知连接和 1 条串行 fetch 连接，manager 的全局 `sync_concurrency` 不变。收到请求且上次观察超过 5 秒时执行共享补查，错误退避带抖动。真实 Apple 联测存在连接仍在但未及时通知新信的情况，因此连接正常和断线状态均采用此观察期限；5 秒不是 Apple 投递或端到端延迟保证，连接、排队和下载仍需时间。`ICLOUD_API_IMAP_IDLE_ENABLED=false` 时回退到单 alias 按需路径；webmail 继续使用原有 callback。`ICLOUD_API_MAIL_ON_DEMAND_ONLY=false` 保留历史兼容行为。

## 上游资源与一致性

1. OTP Bearer、`?token=` 直链、legacy latest/recent 和 pool lease code，以及浏览器直接访问/刷新，复用相同收件工作器。接口格式不变，root 与其合法 `+tag` 聚合；分裂和任务串行由调用项目自行管理。
2. 同一 alias 的请求处理中及完成后 2 秒内，后续请求返回 `429 RATE_LIMITED` 和 `Retry-After: 2`，成功与失败均计入，429 不延后冷却。主号实际 IMAP 获取起点最短间隔为 2 秒；已收取的数据直接读本地，不重复等待这个间隔。
3. 活跃 receiver 最多服务 128 个主号；HTTP 取件最多 128 个在途请求、每主号 64 个，每秒最多接纳 100 次。超量直接返回 429，不排队；alias 仍按请求完成后固定冷却 2 秒，拒绝请求不会延期。`IMAP_IDLE_ENABLED=false` 的旧按需路径仍使用全站 16、每主号 2 的限制。后台和健康检查不占用这些名额。
4. 首次同步、UIDVALIDITY 变化或游标超过 15 分钟时，使用有界的最近 128 条 headers 并按 15 分钟过滤正文；恢复边界写入日志 `recovery_boundary_uid`，早于边界的历史不作完整归档保证。正常活跃增量超过 128 封时分批继续，使用主号持久化 UID 游标，绝不每次重扫尾部。旧单 alias 游标保留用于兼容路径。
5. 上游先批量读取新增 UID 头部，严格核验完整地址优先、首个 + 后标签回根且域名不变，仅命中已登记 alias 才 FETCH 正文；public IMAPS 仅读本地归档。共享工作器不下载未命中地址的正文。
6. 正文使用 PEEK 流式归档，事务提交归档与游标；未提交批次不推进游标。保留 TLS 验证，不记录密码或原始 IMAP 报文。
7. 旧版 recent 入口的消费语义保持不变：消费后通过原有队列回写上游已读标记，失败任务保留重试；这类任务不搜索或下载新邮件。隐藏邮箱创建计划与邮件收取独立。
8. 同步中的通知保留到下一批；读者等待入场时的观察代次，持续来信不会让请求无限等待。断线、提交失败返回可重试结果，不把失败当成已同步；凭据变化或停用时取消旧工作器。客户端断开只取消自己的等待，服务停机则等待共享工作器退出。
9. Pool lease code 仅使用上述专用取件预算，不再重复消耗 Pool 通用的每 IP 每分钟 300 次额度，避免多个 root 合法轮询互相挤占。Pool Key 鉴权、租约归属及状态校验保持不变；其他 Pool 路由保留通用额度，超限携带 `Retry-After: 60`。

## 控制面板

- 普通管理页面空闲时每 30 秒更新一次，正在同步/批量删除时使用 5 秒周期。
- 隐藏标签页或浏览器离线时停止自动请求，回到前台/重新联网时刷新；同一刷新器只允许一个请求在途。
- 请求失败按指数退避至 120 秒，成功恢复普通周期。日志页保持其显式启用的 5 秒刷新。
- 管理页面刷新读取本地数据，不等价于上游 IMAP 读取。正在读取邮件仅对应真实同步任务，不用伪造完成状态来掩盖网络请求。
- 服务端依据 Vite manifest，在首屏 HTML 中仅预载目标页面的本地 JS/CSS 依赖，减少入口脚本到页面脚本的串行发现；不预取其他页面或邮箱 API。没有 manifest 的旧前端产物继续兼容。

## 验证

```sh
flock .local/project-heavy.lock env GOMAXPROCS=2 GOMEMLIMIT=512MiB go test -p 1 -parallel 2 ./internal/mail ./internal/store ./internal/syncer ./internal/httpserver -run 'TestFetchActive|TestFetchAliasIncremental|TestAliasMailboxSync|TestAliasDemand|TestPoolLeaseCodeDemand|TestActiveReceiver' -count=1
```

上线后分别记录冷启动、热缓存、不同 root 并发与新信通知后的取件耗时；检查实际 IMAP 登录/正文读取数量，而非只检查 HTTP 200。
