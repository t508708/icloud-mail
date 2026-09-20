# 来源与许可说明

本仓库 `t508708/icloud-mail` 是 [mangobubu/icloud-api](https://github.com/mangobubu/icloud-api) 的派生版本，保留上游来源与 Git 历史。在其基础上增加和调整了邮箱池、按需取件、Apple Account 连接管理、批量操作、Web 界面与部署工具。

## 许可现状

截至 v0.2.0 发布核查（2026-09-20），上游仓库未提供 MIT、Apache、GPL 等通用软件许可证。本仓库因此未给整个代码库添加新的通用许可证，也不将源码公开等同于授予整个代码库的商业使用或再分发许可。原作者与贡献者保留各自代码的权利。需要这些授权时，请向相关权利人确认；若后续取得明确的统一授权，将更新本文件及许可证。

核查对应上游 `main` 提交 [`2d5d570d8892c7de84a679d7d4e2bbc0360a759a`](https://github.com/mangobubu/icloud-api/tree/2d5d570d8892c7de84a679d7d4e2bbc0360a759a)：文件树及源码归档未发现 `LICENSE` / `COPYING` 许可证文件或 SPDX 许可声明。此提交仅用于固定许可核查证据，不表示本发布已合入该提交的全部功能。

本次发布在 GitHub fork 中进行，只同步经过检查的项目源码。当前服务器的账号、邮件、凭据、证书、运行日志和本机运维历史均未作为发布内容。

## 参考项目

- [q1953258942/iCloud-Privacy-Mail](https://github.com/q1953258942/iCloud-Privacy-Mail)：Apple Account 协议流程参考。
- [cquxl/iCloud-Alias-Hub](https://github.com/cquxl/iCloud-Alias-Hub)：连接续期与管理交互参考；其 `NOTICE.md` 对自身授权另有说明。
- [pyicloud](https://github.com/picklepete/pyicloud)、[ImapFlow](https://github.com/postalsys/imapflow)：会话持久化与 IMAP 同步工程参考。

参考依据和固定版本见 `docs/APPLE-ACCOUNT-RENEWAL.md`、`docs/APPLE-ACCOUNT-RISK-REVIEW.md`、`docs/MAIL-RECEPTION.md`。引用某项目并不表示该项目的许可证覆盖本仓库。

Go 与 npm 依赖按各自许可证分发，版本记录于 `go.mod`、`go.sum` 和 `web/package-lock.json`。预构建应用镜像在 `/usr/share/licenses/icloud-mail/` 保留本说明、Go 标准库许可证和构建依赖中的 LICENSE / NOTICE 等文件及版本索引；基础镜像组件沿用其发行版的授权与来源。本仓库未重新许可这些依赖。本项目为非官方自托管工具，与 Apple Inc. 无隶属或背书关系。
