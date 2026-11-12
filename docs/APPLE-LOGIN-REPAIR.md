# Apple 登录中的手机号补充提示

## 新通道的短信与设备验证

Apple Account 新建连接支持选择短信或受信任设备验证。验证码选项取自新通道自己的认证页，手机号只使用该页返回的编号；只有一个号码或 Apple 已指定的号码时可预选，多个未选号码交给用户选择。页面显示序号和可用尾号。

新通道保留独立 widget、Cookie、SCNT 与登录流程，只与旧通道共用选项解析、发送间隔和请求格式。进入验证页时不额外触发发送；已有短信可以直接选对应手机号提交。发送不明确时保留原登录和冷却，禁止自动重复发送或换号码。通过后继续原有账户修复、信任浏览器与管理会话刷新。

新增管理员接口 `POST <admin-path>/api/v1/accounts/:id/apple-account-auth/code`，参数为 `challenge_id`、`method`（`sms` / `device`）、`phone_id`。原 `/apple-account-auth/verify` 增加可选的 `verification_method` 与 `phone_id`；不传时沿用当前方式。接口要求管理员 Session、CSRF、当前主号下属于该管理员且未过期的登录。重发不延长有效期，发送至少间隔 60 秒，更长 Retry-After 优先。

协议依据分开记录：

- 本地 Account HAR 中，Apple 认证页的 `direct.authInitialRoute` 为 `/auth/verify/phone`，同页返回 `direct.twoSV.phoneNumberVerification.trustedPhoneNumbers`。保存的 Apple HSA2 页面脚本包含按手机号选择短信的界面逻辑。这证明新通道存在手机号验证步骤与选项，不等于已记录一次短信登录成功。
- `q1953258942/iCloud-Privacy-Mail` 提交 `3a839c7a6fb1a2f33b6cc680f4be63ae224fd165` 的 `StartAppleAccountManageLogin` 与 `SubmitAppleAccountManage2FA` 明确实现新通道短信分支；其发送使用 `PUT /verify/phone`，验证使用 `POST /verify/phone/securitycode`，并有 Account 专用的离线用例。完整根路径为认证服务 `/appleauth/auth`。
- 抓包中其他官网脚本存在 `/account/manage/repair/verify/phone...`，它属于账户修复，未作为登录接口的依据。该 HAR 未保存一次完整短信发送、提交成功的请求过程；真实账号效果仍需受控登录确认。

## 旧通道的短信双重验证

iCloud Web 目录连接登录现在先读取 Apple 本次认证页提供的验证选项。读取 JSON 或认证页 `boot_args` 引导数据，不运行页面脚本。

- 只有一个可用短信手机号时，使用 Apple 返回的编号请求短信；多个手机号时等待用户选择，不随机发送。页面仅显示受信任手机号序号及可用尾号。
- 发送短信使用 `PUT /appleauth/auth/verify/phone`，提交短信验证码使用 `POST /appleauth/auth/verify/phone/securitycode`，均带 `mode: "sms"` 与本次认证返回的手机号编号；不会把短信验证码提交到设备验证接口。
- 保留设备验证码方式。若 Apple 未提供可识别的短信选项，仍使用设备流程；设备请求失败不再直接显示已发送。设备接口返回 405 时保留手动输入能力，并说明未确认发送。
- 密码通过后，验证码发送返回错误或结果不明确时保留待验证登录，并显示提醒；已有验证码仍可提交。短信重发入口隐藏时仍保留其认证手机号，多个配置脚本会继续读取到认证选项，不因先出现普通配置而提前停止。
- 验证页可切换短信与设备方式，并在原登录流程中重新发送；请求必须属于当前管理员、当前主号和未过期的登录。重发不延长登录有效期，不重新提交密码。
- 每次发送至少间隔 60 秒；Apple 返回更长 Retry-After 时继续等待该时间。断连或超时不自动重复发送，也不自动换手机号。
- 验证完成后继续原有 repair、信任浏览器和 iCloud 会话交换。自动登录恢复遇到多个待选择的手机号时停止等待人工选择，不随机选取，也不持续索取验证码。
- 日志记录验证方式、可用短信手机号数量、读取与发送的 HTTP 状态和发送是否被接受；不记录完整号码、验证码、密码或 Cookie。

发送与提交格式参考 `q1953258942/iCloud-Privacy-Mail` 的手机验证实现；手机号选项的 `direct.twoSV.phoneNumberVerification` 结构来自本地 HAR。合成案例覆盖这些结构与请求，真实短信是否送达仍取决于 Apple 与手机号的当前状态。

管理接口 `POST <admin-path>/api/v1/accounts/:id/apple-auth/code` 接收 `challenge_id`、`method`（`sms` 或 `device`）和 `phone_id`（短信时必需）。沿用管理员 Session 与 CSRF，手机号编号必须出现在该登录的选项中。成功返回 202 与原登录信息，其中 `verification` 给出当前方式、发送状态、手机号选项和允许再次发送的时间；频率限制返回 429 和 Retry-After。该请求不创建新登录、不延长原登录有效期。

提交接口 `/accounts/:id/apple-auth/verify` 额外接受可选的 `verification_method` 与 `phone_id`，用于直接提交已经收到的短信，不要求先在本服务重发。指定的手机号仍须来自原登录选项；省略这些字段时沿用已选方式。

新 Apple Account 通道与旧 iCloud Web/隐藏邮箱目录通道，均支持登录过程中服务端明确允许跳过的手机号补充流程。对应 Apple 页面中的“不是居住在中国大陆的中国公民？”和“不添加电话号码并继续”。普通账号不额外请求修复页面。

## 处理范围

- 登录提交或验证码验证返回 HTTP 412，且携带当前登录所需的完整 repair 上下文与合法 widget 地址，才进入修复检查。验证码分支还须明确返回 `securityCode.valid=true`，不会重复提交已通过的验证码。
- 修复页跳转地址可以包含浏览器页面片段（`#...`）；发送请求前移除该片段，仍严格校验 HTTPS、固定 Apple 域名、widget 路径及当前登录参数，避免把有效修复流程误判为其他账户待办。
- 仅读取固定 Apple repair widget 的 JSON 引导数据，不执行页面脚本，不下载 JS/CSS。
- 接纳修复页从 `appleid.apple.com` 到 `account.apple.com` 的一次同路径、同参数 HTTPS 页面迁移；仅转发公开导航头，Cookie 仍由隔离的 cookie jar 按目标域管理，其他认证或管理 API 的跳转保持原处理。
- 读取 options 后，只有 `phone_number_requirement`、明确允许跳过、待办仅为手机号及手机号验证，且没有密码、条款或隐私同意等其他待办，才发送一次 `X-Apple-Skip-Repair-Attributes: ["phoneNumber"]`。
- 收到明确完成状态后，提交一次 `/appleauth/auth/repair/complete`。认证上下文与 widget 上下文分别维护，完成请求恢复原认证 SCNT/Session ID，并携带最新 repair token。
- 完成后继续设备信任及原有管理会话刷新或 iCloud `accountLogin`。没有额外的国籍、居住地修改接口，也不新增或修改电话号码。

其他 repair 类型、强制手机号、条款更新、结构不完整或跳过后仍有待办，继续返回需要账户操作的错误，由管理员在 Apple 官网处理。限流、网络失败与不确定结果保持错误及上游重试提示，本流程不循环轮询或自动重放完成请求。

## iCloud 设备信任

验证码验证或支持的 repair 完成后，调用 Apple `/2sv/trust`，保存 Apple 返回的信任状态及 Cookie。后续同账号、同区域的明确登录会复用已加密保存的 trust token，是否免于验证码由 Apple 决定。

若信任已成功而后续 iCloud 会话交换失败，仅保留可供下次登录使用的信任检查点，不将半完成登录发布为已认证。已有正常工作的会话保持其服务状态，独立的新通道会话也保持原样。账号、区域或浏览器身份不匹配时不混用检查点；显式退出按原规则清理信任。

## 验证与限制

协议依据为用户提供的两份本地 HAR：Account 抓包提供 options 和 widget JSON；iCloud 抓包补充旧通道请求及最终受信任状态，但其 repair 响应正文未保存。测试仅使用合成响应，覆盖两种通道的登录/验证码入口、状态隔离、信任保存与失败边界。HAR、账号、Cookie、验证码及令牌不进入源码或测试夹具。

离线通过表示状态机和请求合同已验证，真实账号仍需用该类提示账号完成一次新旧通道登录、同步目录，核对后台认证结果与受信任状态。这一适配不代表已解决新通道约 8 小时的长时失效问题，详见 [保活记录](APPLE-ACCOUNT-RENEWAL.md)。
