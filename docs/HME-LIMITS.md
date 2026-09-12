# Apple HME 创建速率调研

调研日期：2026-09-12。公开源码、作者说明和用户报告各自标注；速率数字不是 Apple 的统一承诺。
本轮未对 Apple Account 新通道做账号登录或每小时吞吐实测，未抓取 iPhone 流量。

## 结论与选型

当前项目保留邮箱池、独立凭据、IMAPS/OAuth 和取码能力，不需要更换整套面板。
优先研究接入 iCloud-Privacy-Mail 的 Apple Account 创建通道，比单纯把本地调度数调到 25 更有依据。
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

## 本部署验证记录

2026-09-12 新增的手动按钮已在公网控制台部署，数量 1-20、串行执行、支持停止后续请求。
真实测试仅发起一次创建：后台返回 `429 APPLE_RATE_LIMITED`，未自动重试；本次没有测得新增地址，也没有测新通道吞吐。
429 是本服务的标准化响应，不能据此断言 Apple 原始 HTTP 状态码也为 429；Apple Web 也可能在响应体中返回限流代码。
模拟响应的浏览器测试验证了首次限流停止、停止按钮保留当前请求、冲突操作禁用及移动端无横向溢出。

`go test ./...`、`go vet ./...`、手动接口 race 测试、前端 186 项测试、Docker 构建、公网健康检查均通过。
发布前一致性备份为 `.local/backups/20260912T103917Z/`，六个文件校验通过；旧镜像保留为 `icloud-api:pre-manual-20260912`。
