# Apple HME 创建速率调研

## 当前创建策略（2026-09-21）

- 自动创建计划按主号使用独立的 rolling 本地预算：每小时最多 23 次，其中 Apple Account 新通道 19 次、iCloud Web 旧通道 4 次，至少间隔 2 分钟并错峰；通道不足时可使用另一通道余量，但不突破任一通道上限。手动批量创建和单次探测不使用本地额度，成功项之间不额外等待，也不修改自动计划或其 Apple 冷却。失败及待确认尝试计入对应通道预算；该策略是本项目的保守上限，不是 Apple 官方额度承诺。
- 自动创建仅在明确限流且未产生地址时切换通道；含 HME、网络不确定或目录确认中的结果不自动补造。创建通道限流默认暂停对应通道 1 小时，更长 Retry-After 以 Apple 为准；验证、目录等公共前置限流仍暂停整个主号 1 小时或更长。手动批量遇明确限流等待默认 1 小时或 Apple 更长，不主动切换；单次手动探测仅返回本次结果，不使用本地额度或间隔。
- 自动计划的本地预算等待表示本项目节流，和 Apple 上游限流是不同状态，界面不得混称。
- 批量创建默认 5 个，仍允许显式调整；后台任务生命周期为 7 天。新建自动计划默认关闭，升级保持已有计划的开启/关闭状态，并更新开启计划的节奏；Apple 冷却保持，旧本地额度等待重新计算。
- 自动计划额度按一次创建尝试递增，失败或结果待确认也计数；页面成功数则只统计已确认创建事件，分为滚动 1 小时和服务时区今日零点至当前，包含手动、批量和自动，排除目录导入；滚动预算不承诺固定完成时长。
- 认证失败应提示先暂停收件同步、更新 App 专用密码并检查邮箱服务状态。

23 个计划槽分布在一小时内，相邻至少 2 分钟；第 6、12、18、23 槽优先旧通道，其余优先新通道，旧通道约每 15 分钟一次。两通道各有持久化滚动小时预算，重启和整点均不清零。升级只重排已开启计划，旧冷却保持；此前未记录通道的最近一小时尝试保守计入两通道一次，等其自然过期。19/4 是安排上限，Apple 返回、库存目标和目录确认可能降低实际成功量。

2026-09-21 验证：提交 `cc5514c` 的干净构建通过 autocreate、hmesync、store、httpserver、cmd/icloud-api 五包测试、创建相关 race 与 vet；一次性 PostgreSQL 测试库验证并发 19/4 额度与冷却重启持久化，前端 59 项聚焦测试通过。更新应用后公网只读验证已开启主号的 23 个剩余计划槽，最小间隔 146 秒；迁移标记与服务健康正常。未额外发送真实 Apple 创建请求，也未完成一整小时 23 个成功地址的吞吐验证。原有未提交 Pool 接口改动未纳入构建。

以下上线记录保留为历史事实；其中每小时 40 次、最短 60 秒、通道自动回落、Apple 冷却 61 分钟及批量默认 30 等旧策略均已被本节取代，不代表当前配置。

调研日期：2026-09-12。公开源码、作者说明和用户报告各自标注；速率数字不是 Apple 的统一承诺。
本项目已完成两个账号的 Apple Account 登录与连续创建测试；以下作者经验值仍不等于固定配额。未抓取 iPhone 流量。

## 结论与选型

当前项目保留邮箱池、独立凭据、IMAPS/OAuth 和取码能力，不需要更换整套面板。
已按 iCloud-Privacy-Mail 的协议线索接入 Apple Account 创建通道，而不是只把本地调度数调到 25。
公开资料存在两种有意义的 25 左右/小时线索：双网页通道，以及 Apple 系统设置 UI 自动化。

| 项目 | 已检查的实现 | 速率证据与结论 |
| --- | --- | --- |
| [mangobubu/icloud-api](https://github.com/mangobubu/icloud-api) | iCloud Web `generate` + `reserve`；本部署基于 `32bd8ccc` | 本地调度每小时 5 次、间隔至少 5 分钟；调度配置与远端限流分开。未发现 Apple Account 管理创建通道 |
| [q1953258942/iCloud-Privacy-Mail](https://github.com/q1953258942/iCloud-Privacy-Mail) | iCloud Web + Apple Account 两套登录态、创建通道及分通道冷却 | 作者 README 称旧约 5、新约 20，合计约 25/小时；有真实调用实现和可选 live 测试源码，未发现证明稳定 25+/小时的完整运行记录 |
| [cquxl/iCloud-Alias-Hub](https://github.com/cquxl/iCloud-Alias-Hub) | README 明确基于上一项目修改，也有相同 Apple Account 创建端点 | 可作为面板实现参考；未发现其独立提高配额的证据 |
| [rtunazzz/hidemyemail-generator](https://github.com/rtunazzz/hidemyemail-generator) | CLI、macOS、Android 与冷却调度 | 当前 README 是每家庭成员约 5/30 分钟的观察值；项目 app 仍遵循限流。issue #40 有系统设置自动化的另一条线索 |
| [D3-vin/icloud-hidemyemail-generator](https://github.com/D3-vin/icloud-hidemyemail-generator) | Chrome TLS 指纹，仍调用 `maildomainws /v1/hme` | README 仍写约 5/30 分钟/家庭成员；TLS 指纹不是高配额证明 |
| [LYH105/iCloudEmail-Lite](https://github.com/LYH105/iCloudEmail-Lite) | 前端可批量 1-25，后端仍为 `generate/reserve` | 25 是单批输入上限，不是每小时实测吞吐 |
| [forever94yu/icloud-prime](https://github.com/forever94yu/icloud-prime) | Web 创建及本地每小时计数 | README 明写每账号每小时最多成功 5 个 |
| [heartmore/icloud-hme](https://github.com/heartmore/icloud-hme) | 同类 Web 端点与调度器 | 所查源码未提供独立新创建通道或稳定 25+/小时记录 |

## Apple Account 新通道：不需要 iPhone 抓包

参考版本：`q1953258942/iCloud-Privacy-Mail@3a839c7a6fb1a2f33b6cc680f4be63ae224fd165`。

- [README 登录态与额度说明](https://github.com/q1953258942/iCloud-Privacy-Mail/blob/3a839c7a6fb1a2f33b6cc680f4be63ae224fd165/README.md#L173-L190)
- [生成、提交和确认源码](https://github.com/q1953258942/iCloud-Privacy-Mail/blob/3a839c7a6fb1a2f33b6cc680f4be63ae224fd165/internal/app/icloud_client.go#L233-L350)
- [通道选择和回落](https://github.com/q1953258942/iCloud-Privacy-Mail/blob/3a839c7a6fb1a2f33b6cc680f4be63ae224fd165/internal/app/server.go#L3565-L3662)
- [可选 live 测试源码](https://github.com/q1953258942/iCloud-Privacy-Mail/blob/3a839c7a6fb1a2f33b6cc680f4be63ae224fd165/internal/app/apple_account_live_test.go)

源码的管理 API 默认请求主机为 `appleid.apple.com`，Portal Origin 为 `https://account.apple.com`，另有 `.com.cn` 适配。

```text
POST /account/manage/email/private/add
  {}
  -> emailAddress (candidate)

PUT /account/manage/email/private/add/complete
  {"emailAddress":"CANDIDATE","label":"LABEL","note":"NOTE"}
  -> id, emailAddress, active

GET /account/manage/email/private/REMOTE_ID.em
  -> confirmation and forwarding data
```

这是真正的第二套网页管理接口，并非已经证明的 iOS 私有接口。
需要独立 Apple Account 登录/2FA，管理 Cookie、`scnt`、动态 `X-Apple-Api-Key`；已有 iCloud Web 会话不能简单等同于这套管理态。
参考实现按返回 TTL 更新管理态，并有 token、portal、apiKey 刷新步骤。把 URL 或 User-Agent 换一下并不完成这条认证链。

集成时需要单独加密存储管理态，按账号/通道记录成功和限流；任何创建结果不明时先查询确认，再考虑其他通道，避免双重创建。
应保留当前邮箱池与凭据生成/归属校验，只替换远端创建适配层。完成登录后先验证一条创建到本地凭据的链路，再测连续 60 分钟、跨窗口是否稳定。

## 手机体验的公开旁证

[rtunazzz issue #40](https://github.com/rtunazzz/hidemyemail-generator/issues/40) 与用户描述相近：提问者称 Web 限流，而 iPhone 不同。

- [用户报告约 20-30/小时](https://github.com/rtunazzz/hidemyemail-generator/issues/40#issuecomment-4229320247)：措辞是个人观察，并无完整日志。
- [用户报告 MacBook Pro 每两小时 50-60](https://github.com/rtunazzz/hidemyemail-generator/issues/40#issuecomment-4229479609)：平均约 25-30/小时，不等于任意滚动一小时都达到 25。
- [随后贴出的 AppleScript](https://github.com/rtunazzz/hidemyemail-generator/issues/40#issuecomment-4350164349)：自动操作 macOS 系统设置，进入 iCloud/Hide My Email、创建、填写标签并提取结果。不是仓库 macOS app，也不是公开 iPhone HTTP 调用脚本。UI 索引及按钮名依赖系统版本和语言。
- [维护者后续说明](https://github.com/rtunazzz/hidemyemail-generator/issues/40#issuecomment-5083620823)：Web 约 5/30 分钟/家庭成员。各人的数据不同，进一步说明经验值不适合作为统一配额。

用户观察的换全局 IP 后恢复，与出口参与限流相符，但尚不能排除时间窗口、认证会话、客户端路径、家庭成员或其他变量。
服务端项目的 Apple 请求从服务器发出：只切手机代理并不改变服务器出口。Cloudflare 网站橙云也只处理访问本站的入站流量。
要验证因果，应固定账号、通道、会话和时间记录，一次仅改变出口；以目录中确认的新地址数量计数，而不是候选地址或按钮点击次数。

## 后台批量创建任务（新实现）

管理端已提供 1–100 个地址的持久化后台任务，默认数量 30，通道为 `auto`、`apple_account` 或 `icloud_web`。`auto` 优先使用 Apple Account，只有限流时回落旧 Web 通道；Apple Account 有独立登录/2FA，会话仍需旧 Web 登录完成本地归属同步。

任务遇到明确限流至少等待 61 分钟，总时限 24 小时；每 4 分钟尝试管理 session 保活。停止只阻止后续请求，进行中的请求完成并保存后停止。应用重启后的 `interrupted` 任务不会自动重发。定时创建同样自动选择双通道，最多 3 个主号并行，同一主号共享串行锁。

本轮上线验证（2026-09-12）：

- 公网管理面板默认 30、上限 100，模拟任务开始/停止和 390px 手机宽度检查通过。
- 真实旧 Web 通道提交目标 30，成功创建 2 个地址（本地 ID 899、900），随后进入限流等待；测试结束后停止任务，保留这 2 个地址。
- 逐一从管理 API 读回新地址，验证 API Key、IMAP 密码、client_id、refresh_token 齐备；没有实际收取测试邮件。
- 新通道匿名 Portal 初始化发现文档 GET 携带 `Content-Type: application/json` 会返回 HTTP 500；去除此文档请求头后，隐私页面及 `/bootstrap/portal` 两步均返回 HTTP 200。此验证没有登录或创建地址。
- 用户完成两个主号的新通道登录/2FA 后，主号 1 的新通道任务成功 20 个、主号 2 的自动双通道任务成功 19 个，随后各有一个未确认候选，任务停止。失败原始响应未被记录，尚未确定原因，不能据此认定为固定 20 个配额。
- 后续单次新通道诊断创建又成功 1 个。对此前两个候选（939、942）多次查询目录均未发现，维护窗口中保留禁用别名和加密凭据，仅退休待确认标记/领取队列，未删除 Apple 邮箱。恢复脚本保存在 `.local/account_diagnostic_test.go`，此前一致性备份 `.local/backups/20260912T125050Z/`。
- 再次通过真实后台任务补建 9 个、11 个均完成。本轮合计新增 60 个：主号 1 为 20+1+9，主号 2 为 19+11；排除并行定时调度另外创建的地址。不是一次无中断创建 60 个，也不是每小时固定配额保证。
- 对全部 60 个新地址逐一验证已启用、归属主号正确且完整凭据齐备；各主号抽取一个新地址，公网 API Key、OAuth refresh 换令牌、IMAPS 密码登录与 XOAUTH2 登录均通过，自动入池状态为 available。验证摘要保存 `.local/batch-final-verification.json`。
- 增加批量任务失败阶段与 Apple HTTP 状态日志，不记录密码、Cookie 或原始响应体，方便后续定位偶发未确认结果。
- 跨主号并发调度已上线，`WithConcurrency` 默认 3（可注入 1-16）；并发上限、同号去重、取消后等待 worker 退出均通过 race 测试。当前每主号每小时 40 次计划，最短 60 秒、平均约 90 秒；双通道选择与跨主号并发保留，Apple 实际限流冷却 61 分钟仍生效；后台批量任务不受该节奏限制。
- 最新备份 `.local/backups/20260912T125944Z/` 六项校验通过，部署后两个管理会话仍已登录、最近两个补建任务均为 completed，公网健康检查和移动端面板通过。
- Go 全量测试、关键创建与停止 race 测试、`go vet`、前端 188 项测试及生产构建通过。
- 一致性备份 `.local/backups/20260912T123213Z/` 六项校验通过；发布前二进制和前端另存 `.local/pre-batch-20260912/`，此前回滚镜像 `icloud-api:pre-manual-20260912` 仍保留。

## 定时计划升级为每主号 40 次/小时

- 2026-09-12 已部署：每个主号独立生成 40 个计划槽，计划从起点到最后一槽恰好 1 小时，最短间隔 60 秒、平均 90 秒；轮询间隔缩短至 5 秒。
- 启动时通过事务和版本标记 `40-per-hour-v1` 一次性替换已开启的旧计划，保留历史字段和已有冷却；关闭的计划不变，后续重启不重置计划。任一步失败时计划和版本标记一起回滚。
- 公网后台读回两个主号均已开启、各有 40 个计划槽，Apple Account 会话均为 authenticated，页面已显示新频率。主号 1 保留此前限流冷却；主号 2 在新计划首槽已成功创建（本地 ID 966），下一槽结果进入目录待确认，后续计划先继续确认。
- 40 是每主号每小时的本地尝试计划，不是已实测持续一小时成功 40 个；库存目标、同主号锁、结果确认和 Apple 限流仍生效。
- 部署前一致性备份 `.local/backups/20260912T131722Z/` 六项校验通过；Go 全量测试、`go vet`、调度器与存储 race 测试、前端 188 项测试、生产构建及公网健康检查通过。

## 2026-09-12 待确认误判修复

- 实测 Apple Account `PUT /account/manage/email/private/add/complete` 返回 HTTP 412，JSON 中 `ineligibilityReason=rate_limit_exceeded`、`ineligibilityType=settings`、`active=false`、`exists=false`。此前解析器遗漏该字段，将未成功的候选保存为待确认，后续计划一直只查目录。
- 现在按这一明确原因识别限流：自动模式回落 iCloud Web；明确选择新通道时也显示限流。未知的 412 和网络中断继续保留候选供目录确认，避免重复提交。
- 待确认文案修正为“Apple 创建结果尚未完成目录确认”，不再提前声称地址已创建。
- 历史候选经两次目录核对：本地 968 已被 Apple 再次分配并成功创建，恢复其原凭据、启用并入池；939、942、967、969 均未出现，将其本地未发布记录移出以避免再次分配时发生地址唯一键冲突，未调用 Apple 删除接口。主号 2 定时计划已恢复为 40 个槽。
- 操作前一致性备份 `.local/backups/20260912T132738Z/` 和 `.local/backups/20260912T134306Z/` 六项校验通过；恢复源码存 `.local/retired_candidate_repair_test.go`，保留原记录的加密凭据备份。
- 批量创建栏移至“隐私邮箱”标题紧接下一行；桌面标题、数量、通道和操作按钮同排，任务状态与结果独立展示，手机屏幕自动换行。
- 两个主号恢复后均在定时计划中成功创建，主号 2 的新地址为本地 978；待确认队列为 0。浏览器验证 1440px 桌面控件同排、390px 手机无横向溢出，两账号计划开启且无最近错误，验证摘要 `.local/pending-layout-verification.json`。
- Go 全量测试、`go vet`、Apple/HME/调度器 race 测试、前端 189 项测试和生产构建通过。专门回归 HTTP 412 明确限流、其他数字错误码共存和未知原因，确保未知结果仍保留候选。

## 此前手动按钮验证记录

2026-09-12 新增的手动按钮已在公网控制台部署，数量 1-20、串行执行、支持停止后续请求。
真实测试仅发起一次创建：后台返回 `429 APPLE_RATE_LIMITED`，未自动重试；本次没有测得新增地址，也没有测新通道吞吐。
429 是本服务的标准化响应，不能据此断言 Apple 原始 HTTP 状态码也为 429；Apple Web 也可能在响应体中返回限流代码。
模拟响应的浏览器测试验证了首次限流停止、停止按钮保留当前请求、冲突操作禁用及移动端无横向溢出。

`go test ./...`、`go vet ./...`、手动接口 race 测试、前端 186 项测试、Docker 构建、公网健康检查均通过。
发布前一致性备份为 `.local/backups/20260912T103917Z/`，六个文件校验通过；旧镜像保留为 `icloud-api:pre-manual-20260912`。
