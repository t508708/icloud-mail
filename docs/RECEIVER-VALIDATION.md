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

## 真实取件及联测

- 优化前连续三次公网取件耗时 2408 / 2748 / 2392 ms。
- 首次唤醒需连接 Apple，实测 4545 ms；后续热取件 26 / 26 ms，另一组热读 24 / 23 ms。协作客户端热读记录为 6 ms。热读不是每次请求的时延保证，补查、连接及上游排队仍需等待。
- 成功取件后，在 0 / 300 / 600 / 900 / 1200 / 1500 / 1800 ms 持续刷新均为 429；2301 ms 时恢复 200，证明拦截不推迟冷却结束。
- 第一受控任务于 03:55:01.462 确认发信，最终 root 读到邮件时间 03:55:08。
- 第二受控任务于 04:04:42.337 确认发信；04:05:46.764 开始的取件耗时 3663 ms，返回邮件时间 04:04:46，新归档 UID 大于上次游标。两轮 root 新邮件均验证，未以旧码或 HTTP 200 替代判断。
- 协作项目两次都配置等待 90 秒，但约 9 秒内两次空响应即触发 `otp_timeout`。已向其维护会话交接事实和修复建议：单调时钟总 deadline、空结果继续等待、保留新邮件时间过滤和同 root 串行。本项目未修改协作项目代码。
- 两轮真实 Apple 新信未及时通过 IDLE 触发归档，后续请求补查才发现。依据这个运行证据，最终版本将正常连接下的按请求共享补查期限从 30 秒缩为 5 秒，并测试静默连接边界；无人请求仍不周期补查。真实 Apple 的即时 IDLE 和真实 `+tag` 新邮件投递不列为本轮已验证成功项，`+tag` 路由及 IDLE 通路已有离线覆盖。
- 本轮总共只执行两个受控任务，没有新增 root、删除邮箱或扩大分裂数量。协作任务全流程成功仍待其等待逻辑修复后再验收。

## 后台页面

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

## 保留边界

- 不扫描无限历史：恢复仅末尾 128 条头部，正文限最近 15 分钟；常规活跃增量继续持久主号游标。
- 同 root 完成后 2 秒冷却；HTTP 全局/主号准入和实际 IMAP 并发分别有界，超额不积压无限队列。
- 不改变 root 分裂/串行规则、既有取件响应结构、Pool 生命周期或旧管理删除合同。
- 本轮使用干净提交构建，已有其他 Pool retirement / claimable accounts 工作树改动保留且未混入发布。
