# 按需触发的邮件接收

## 选型

保留现有 Go + PostgreSQL + MIME 文件归档，不增加 Node.js、Redis 或另一个邮件平台。
参考的是成熟项目的协议与状态管理设计，并非整体部署或拷贝它们的实现：

- [ImapFlow](https://github.com/postalsys/imapflow)：MIT 许可，支持 IDLE、邮箱锁和流式读取，以及按服务器能力启用的 CONDSTORE/QRESYNC 等扩展。
- [go-imap](https://github.com/emersion/go-imap)：本项目已使用的 Go IMAP 协议库；IDLE 命令自动约每 28 分钟续订，避免长期空闲超时。
- [EmailEngine](https://github.com/postalsys/emailengine)：参考通知唤醒与持久化同步状态的分工。其当前许可为商业源码可用许可，项目没有将它当作免费开源依赖引入。

默认 `ICLOUD_API_MAIL_ON_DEMAND_ONLY=true`：不在启动时或周期任务中建立 IMAP 同步/IDLE。邮件读取由真实、已鉴权的取件请求触发；管理员显式手动同步仍可执行主号级增量同步。`ICLOUD_API_MAIL_ON_DEMAND_ONLY=false` 时保留历史的周期/IDLE 兼容行为。

## 上游资源与一致性

1. OTP Bearer、`?token=` 直链、legacy latest/recent 和 pool lease code，以及浏览器直接访问/刷新取件地址，都会触发对应 alias 的读取。
2. OTP、浏览器取件链接、旧邮件 API 和邮箱池取码按同一 alias 共用限流：请求处理中及完成后 3 秒内，后续请求直接返回 `429 RATE_LIMITED` 和 `Retry-After: 3`，成功与失败均计入。同步层保留 3 秒去重，同主号原有最短 30 秒 fetch guard 保留。
3. 取件入口在认证和数据库访问前，每秒最多接纳 100 次、全站最多同时处理 16 个请求；每主号最多容纳 2 个已认证取件请求。超量直接返回 429，不排队；后台和健康检查不占用这些名额。限流记录有容量上限，429 日志按路由采样。限制在单个服务进程内生效，重启后重新计数。
4. 每次最多处理一批 128 封目标邮件，不后台续跑；首次按最近 4096 个 UID 的数值窗口读取，后续访问从每个 alias 自己的游标继续。
5. 上游先 SEARCH recipient headers，再精确复核归属，只 FETCH 目标邮件内容；public IMAPS 仍仅读本地归档。
6. 正文使用 PEEK 流式归档，事务提交归档与游标；未提交批次不推进游标。保留 TLS 验证，不记录密码或原始 IMAP 报文。
7. 旧版 recent 入口的消费语义保持不变：消费后通过原有队列回写上游已读标记，失败任务保留重试；这类任务不搜索或下载新邮件。隐藏邮箱创建计划与邮件收取独立。

## 控制面板

- 普通管理页面空闲时每 30 秒更新一次，正在同步/批量删除时使用 5 秒周期。
- 隐藏标签页或浏览器离线时停止自动请求，回到前台/重新联网时刷新；同一刷新器只允许一个请求在途。
- 请求失败按指数退避至 120 秒，成功恢复普通周期。日志页保持其显式启用的 5 秒刷新。
- 管理页面刷新读取本地数据，不等价于上游 IMAP 读取。正在读取邮件仅对应真实同步任务，不用伪造完成状态来掩盖网络请求。

## 验证

```sh
flock .local/project-heavy.lock env GOMAXPROCS=2 GOMEMLIMIT=512MiB go test -p 1 -parallel 2 ./internal/mail ./internal/store ./internal/syncer ./internal/httpserver -run 'TestFetchAliasIncremental|TestAliasMailboxSync|TestAliasDemand|TestPoolLeaseCodeDemand' -count=1
```

上线后验证一次真实取件请求能触发单 alias 读取，并确认并发合并、游标续读和 public IMAPS 本地读取行为。
