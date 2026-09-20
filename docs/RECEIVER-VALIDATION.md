# 共享收件验证记录

验证日期：2026-09-20。所有时间为 UTC+08:00；证据不包含取件凭据、验证码或完整收件地址。

## 离线覆盖

- TLS IMAP fixture 下 40 个 root 同时通过真实 HTTP 入口取件，合法 `+tag` 聚合到各自 root；不同域名与陈旧正文不下载。初始正文恰好读取 40 次，IDLE 新信后总计 41 次。
- Pool lease code 和派生取件链接共享同 root 冷却；Pool 鉴权、项目/租约隔离继续由原流程校验。
- 同主号合并、跨主号独立、单读者取消、同步中通知、持续通知、断线与静默连接补查、失败退避不被刷新延期、容量上限、休眠/唤醒、凭据变化、停用与删除主号。
- 初次与 UIDVALIDITY 恢复有界；正常活跃期间 140 封消息分批归档，不因尾部边界跳过；已提交游标不重复读取正文。
- 目标页面预载仅使用本地 Vite manifest 的资源；旧 bundle、循环依赖和非法资源路径兼容验证。

```sh
flock .local/project-heavy.lock env GOMAXPROCS=2 GOMEMLIMIT=512MiB GIN_MODE=release go test -p 1 -parallel 2 ./internal/mail ./internal/syncer ./internal/httpserver ./internal/store ./cmd/icloud-api
flock .local/project-heavy.lock env GOMAXPROCS=2 GOMEMLIMIT=512MiB GIN_MODE=release go test -race -p 1 -parallel 2 ./internal/mail ./internal/syncer ./internal/httpserver -run 'TestFetchActive|TestActiveReceiver|TestPoolPickup|TestAdminRoutePreloads|TestPickupRate' -count=1
flock .local/project-heavy.lock env GOMAXPROCS=2 GOMEMLIMIT=512MiB go vet -p 1 ./internal/mail ./internal/syncer ./internal/httpserver ./internal/store ./cmd/icloud-api
# 在 web 目录执行；使用同一项目锁。
flock ../.local/project-heavy.lock node --test --test-concurrency=2
git diff --check
```

以上受影响 Go 包、聚焦 race、vet 与 230 个前端测试通过；干净提交快照的 Go 测试和 Docker/Vite 构建也通过。

## 真实取件及联测：首轮

- 优化前连续三次公网取件耗时 2408 / 2748 / 2392 ms。
- 首次唤醒需连接 Apple，实测 4545 ms；后续热取件 26 / 26 ms，另一组热读 24 / 23 ms。协作客户端热读记录为 6 ms。热读不是每次请求的时延保证，补查、连接及上游排队仍需等待。
- 成功取件后，在 0 / 300 / 600 / 900 / 1200 / 1500 / 1800 ms 持续刷新均为 429；2301 ms 时恢复 200，证明拦截不推迟冷却结束。
- 第一受控任务于 03:55:01.462 确认发信，最终 root 读到邮件时间 03:55:08。
- 第二受控任务于 04:04:42.337 确认发信；04:05:46.764 开始的取件耗时 3663 ms，返回邮件时间 04:04:46，新归档 UID 大于上次游标。两轮 root 新邮件均验证，未以旧码或 HTTP 200 替代判断。
- 协作项目两次都配置等待 90 秒，但约 9 秒内两次空响应即触发 `otp_timeout`。已向其维护会话交接事实和修复建议：单调时钟总 deadline、空结果继续等待、保留新邮件时间过滤和同 root 串行。本项目未修改协作项目代码。
- 两轮真实 Apple 新信未及时通过 IDLE 触发归档，后续请求补查才发现。依据这个运行证据，最终版本将正常连接下的按请求共享补查期限从 30 秒缩为 5 秒，并测试静默连接边界；无人请求仍不周期补查。真实 Apple 的即时 IDLE 和真实 `+tag` 新邮件投递不列为本轮已验证成功项，`+tag` 路由及 IDLE 通路已有离线覆盖。
- 首轮总共只执行两个受控任务，没有新增 root、删除邮箱或扩大分裂数量。当时协作任务全流程成功仍待其等待逻辑修复后再验收。

## 协作端等待窗口修复

由 GPT-Free-Auth 维护会话实施，本项目仅协调与只读审查，不直接编辑其工作区：

- `1072bb9`、`d8a83de`：按单调时钟维护总 deadline，删除固定尝试次数导致的提前退出；空结果和早于 `since` 的邮件继续等待，`Retry-After` 截断至剩余预算；managed RPC 每次事务使用一个完整取码窗口，普通邮箱行为保持。
- 审查进一步确认同 root 队列等待未受当前读者 deadline 约束。`1c3ddce`、`3ebe631` 补齐等待者到期返回、过期排队项不补发 HTTP、其他读者保持串行、取消及时清理 timer。确定性测试使用手动时钟与 timer，而非依赖短暂真实睡眠。
- 维护方回报 24/24 聚焦测试、构建、工程门禁及 diff check 通过，已部署 `3ebe631`，任务执行前服务健康且无活跃任务；本项目网关同时 healthy。
- 不改变租约归属、root 分裂规则或验证码时间过滤；不把 API 返回 200 作为任务成功证明。

### 单样本真实复验

只提交一次人工重试，复用现有 alias `621`，没有新增 root 或 tag，也没有删除邮箱。维护会话提供的脱敏日志与网关归档证据如下：

- 11:16:22.405 确认发信；第 1 次取件 HTTP 503、2789 ms、`Retry-After: 3`；第 2 次 HTTP 200、2974 ms。
- 首次轮询游标仍为 UID 75、零新邮件；UID 76 于 11:16:30.845 新增归档，11:16:31.525 出现 `mailbox_code_received`，确认发信至取码约 9.12 秒。
- 邮件 Date 头为 11:16:21，精度为秒，与发信开始同秒；结合新 UID 和归档时序证明是本次新邮件，而非旧码。首段取码已成功，不以单次 HTTP 200 代替这个判断。
- 后续密码补齐开启独立邮箱事务，快照为 11:16:41。其读到 UID 77 的邮件时间为 11:16:33，早于快照，被 `since` 正确过滤。随后 17 次 HTTP 200 均无 Retry-After，耗时 5-3675 ms；这只是成功查询，不表示存在符合时间条件的新验证码。
- 第二事务实际等待完整 90 秒，11:18:16 超时，最终失败阶段为 `password_completion_email_otp_timeout`。首段取码修复成立，但整单任务未通过；第二事务的时间基线与发信顺序留在协作项目排查，本网关不放宽 `since` 以接纳旧码。
- 另观察到协作端在 11:19:55 自动将同一任务 retryCount 从 9 推到 10 并再次执行，维护会话没有再次提交重试。该重复调度是独立问题，本次未扩展修改。自动执行随后结束，维护方最终核对 activeTasks 为 0，双方服务健康。这里的“一次”仅指人工重试次数，并非宣称系统总共只执行一次。

## 后台页面：首轮资源预载

资源全部同源，测试未发现外部 JS/CSS 请求或浏览器异常。服务器经公网域名读取后台 API 为 13-26 ms，当前证据不指向数据库列表查询为主要瓶颈。

| 场景 | 优化前，180 ms 延迟 / 200 KiB/s | 优化后，同模拟参数 | 优化后，服务器本地浏览器不额外加延迟 |
| --- | ---: | ---: | ---: |
| 主号首次打开 | 2854 ms | 2617 ms | 697 ms |
| 主号刷新 | 1033 ms | 1038 ms | 188 ms |
| 隐私邮箱首次切换 | 1117 ms | 1101 ms | 502 ms |
| 切回主号 | 366 ms | 364 ms | 138 ms |
| 邮箱池首次切换 | 1009 ms | 872 ms | 311 ms |
| 邮箱池刷新 | 1104 ms | 1125 ms | 263 ms |

这组样本说明资源发现瀑布已缩短：主号页面依赖从入口脚本执行后约 1412 ms 才发出，提前到 HTML 到达时约 360 ms 发出。刷新没有显著改善，不能宣称所有切换都大幅加速。前后 CDN 资源分别存在 MISS/HIT 差异，因此上述时差不是严格隔离变量的收益估计；本地浏览器数字也不代表用户所在地网络。

## 后台刷新补充修复

首轮资源预载没有改善刷新，因此继续追踪 HTML、入口脚本、会话查询、列表查询及首次可见时间，而非仅根据 HTTP 200 验收。

- `132ae2b`：有效管理会话在私有、禁止缓存的 HTML 中提供现有 session DTO，首屏复用后移除节点，省掉串行 `/auth/session` 请求。数据 API 仍各自鉴权；过期、撤销、轮换、退出及 CSRF 边界保持。
- `d9560ea`：生产浏览器验收发现默认参数中的可选链经转译后产生 `ReferenceError`，前一版仍回退会话 API。将取时钟移入函数体并保持原生 receiver，增加实际 esbuild/minify 产物回归测试；重新构建后再次验收，而非把源码测试通过视为发布成功。
- 运行容器确认版本 `d9560ea` 且 healthy。生产浏览器验证：HTML 含引导、使用后 DOM 节点数为 0、首次列表加载的 `/auth/session` 请求数为 0。无外部 JS/CSS 请求或 pageerror。

### 同版本开关对照

真实公网入口、同一个浏览器上下文、相同生产 bundle、已热缓存资源；模拟延迟 180 ms、下载 200 KiB/s。仅在测试浏览器中通过 MutationObserver 去掉 HTML 引导节点来恢复旧的会话请求路径，不修改线上服务器。按关、开、开、关、关、开交错测量，每个页面每种模式各 3 次，并断言关时 1 次、开时 0 次 session 请求。

指标为导航后 `requestAnimationFrame` 首次观察到列表行可见的时间，不使用可能包含额外轮询等待的 Playwright `waitFor` 返回时间。

| 页面刷新 | 关闭引导，3 次 | 启用引导，3 次 | 中位数变化 |
| --- | --- | --- | --- |
| 主号管理 | 650 / 637 / 659 ms | 448 / 452 / 442 ms | 650 -> 448 ms，减少约 31% |
| 邮箱池 | 726 / 722 / 718 ms | 530 / 554 / 528 ms | 722 -> 530 ms，减少约 27% |

独立前后测量同样参数下，主号刷新首次可见 640 -> 470 ms，邮箱池 742 -> 558 ms，与对照结果一致。服务器本地浏览器不额外施加延迟时，新版单次测得 154 / 281 ms；这些数字不代表其他地区网络，亦不承诺所有页面同等收益。

本地证据仅包含请求路径、耗时和缓存状态：`.local/refresh-ab-profile.json`、`.local/page-load-refresh-before-observed.json`、`.local/page-load-refresh-fixed-first.json`、`.local/page-load-refresh-fixed-local.json`。测试脚本 `.local/bootstrap-smoke.mjs` 和 `.local/refresh-ab-profile.mjs` 使用本地凭据，不把凭据写入结果或版本库。

补充验证：受影响 Go 包 `./internal/httpserver ./cmd/icloud-api`、聚焦 `TestAdminSessionBootstrap|TestAdminSPA|TestAdminRoutePreloads` race、对应 `go vet`、236 个前端测试及干净快照 Docker/Vite 构建通过。发布后的 `d9560ea` 干净快照再次通过上述两个 Go 包测试和 vet，排除已有 Pool 工作树改动对结果的影响。聚焦回归命令：

```sh
flock .local/project-heavy.lock env GOMAXPROCS=2 GOMEMLIMIT=512MiB GIN_MODE=release go test -p 1 -parallel 2 ./internal/httpserver ./cmd/icloud-api
flock .local/project-heavy.lock env GOMAXPROCS=2 GOMEMLIMIT=512MiB GIN_MODE=release go test -race -p 1 -parallel 2 ./internal/httpserver -run 'TestAdminSessionBootstrap|TestAdminSPA|TestAdminRoutePreloads' -count=1
flock .local/project-heavy.lock env GOMAXPROCS=2 GOMEMLIMIT=512MiB go vet -p 1 ./internal/httpserver ./cmd/icloud-api
# 在 web 目录执行。
flock ../.local/project-heavy.lock node --test --test-concurrency=2
```

## 保留边界

- 不扫描无限历史：恢复仅末尾 128 条头部，正文限最近 15 分钟；常规活跃增量继续持久主号游标。
- 同 root 完成后 2 秒冷却；HTTP 全局/主号准入和实际 IMAP 并发分别有界，超额不积压无限队列。
- 不改变 root 分裂/串行规则、既有取件响应结构、Pool 生命周期或旧管理删除合同。
- 本轮使用干净提交构建，已有其他 Pool retirement / claimable accounts 工作树改动保留且未混入发布。
