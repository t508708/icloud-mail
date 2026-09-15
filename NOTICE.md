# 来源与许可说明

本仓库 `t508708/icloud-mail` 是 [mangobubu/icloud-api](https://github.com/mangobubu/icloud-api) 的派生版本，保留上游来源与 Git 历史。在其基础上增加和调整了邮箱池、按需取件、Apple Account 连接管理、批量操作、Web 界面与部署工具。

## 许可现状

截至本次公开发布，上游仓库未提供 MIT、Apache、GPL 等通用软件许可证。本仓库因此未给整个代码库添加新的通用许可证，也不将源码公开等同于授予整个代码库的商业使用或再分发许可。原作者与贡献者保留各自代码的权利。需要这些授权时，请向相关权利人确认；若后续取得明确的统一授权，将更新本文件及许可证。

本次发布在 GitHub fork 中进行，只同步经过检查的项目源码。当前服务器的账号、邮件、凭据、证书、运行日志和本机运维历史均未作为发布内容。

## 参考项目

- [q1953258942/iCloud-Privacy-Mail](https://github.com/q1953258942/iCloud-Privacy-Mail)：Apple Account 协议流程参考。
- [cquxl/iCloud-Alias-Hub](https://github.com/cquxl/iCloud-Alias-Hub)：连接续期与管理交互参考；其 `NOTICE.md` 对自身授权另有说明。
- [pyicloud](https://github.com/picklepete/pyicloud)、[ImapFlow](https://github.com/postalsys/imapflow)：会话持久化与 IMAP 同步工程参考。

参考依据和固定版本见 `docs/APPLE-ACCOUNT-RENEWAL.md`、`docs/APPLE-ACCOUNT-RISK-REVIEW.md`、`docs/MAIL-RECEPTION.md`。引用某项目并不表示该项目的许可证覆盖本仓库。

Go 与 npm 依赖按各自许可证分发，版本记录于 `go.mod`、`go.sum` 和 `web/package-lock.json`。本仓库未重新许可这些依赖。本项目为非官方自托管工具，与 Apple Inc. 无隶属或背书关系。
