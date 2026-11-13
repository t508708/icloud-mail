<div align="center">

**简体中文 | [English](README.en.md)**

# iCloud Mail

**把 iCloud 隐藏邮箱管理、邮件接收和 API 取码放进一个控制面板。**

自托管 · 多主号 · 邮箱池 · 按需收件 · 深浅色主题

**当前版本：v0.2.11**

[下载与发布说明](https://github.com/t508708/icloud-mail/releases) · [Docker 部署](#docker-部署) · [接口文档](docs/POOL.md) · [更新日志](CHANGELOG.md)

</div>

适合希望集中管理 iCloud 隐藏邮箱、自动补充地址库存，或为自己的工具接入收件与验证码 API 的用户。通过 Web 面板完成日常操作，也可以用邮箱池接口领取、使用和退役地址。

## 能做什么

| 能力 | 使用体验 |
| --- | --- |
| **一个面板管理多个主号** | Apple 风格界面，支持 Liquid Glass 效果与深浅色模式；JS、CSS 随服务本地提供。 |
| **隐藏邮箱批量管理** | 同步地址、搜索筛选、跨页选择、框选多选、批量创建与删除。 |
| **自动补充地址** | 手动批量创建与定时**自动创建计划**并存；新旧双通道按各自额度和状态工作。 |
| **项目邮箱池** | 为不同项目签发 Pool Key，提供领取、批量确认使用、续租、释放与退役接口；可查看启用主号的可领取数量。 |
| **链接与 API 取码** | 每个邮箱有独立取件入口；识别多语言验证码邮件，并支持将已登记根邮箱的 `+tag` 邮件归入根邮箱取件。 |
| **按需接收邮件** | 访问取件链接或 API 时触发增量读取，同一主号复用收件连接，减少重复请求和历史正文下载。 |
| **每主号独立代理** | 新建或编辑主号时启用专属 HTTP、HTTPS 或 SOCKS5 代理，支持前置代理接力，覆盖 Apple 登录、保活、IMAP 收件与连接恢复。 |
| **独立凭据与连接恢复** | 每邮箱独立 API Key、IMAP 密码及 OAuth 凭据，支持轮换；Apple Account 与 iCloud Web 登录可选择设备验证或受信任手机号短信，按保存的信息自动恢复连接。 |

**凭据说明**：独立 IMAP/OAuth 凭据由本网关签发，用于连接本服务；连接 Apple 上游邮箱仍需对应主号的收件凭据。

### 主号代理

在「添加主号」或「编辑主号」中开启「主号代理」，填写落地代理地址；可选填前置代理，形成 `服务器 → 前置代理 → 落地代理 → Apple / IMAP` 的连接路径。单击「测试代理」检查能否通过该链路访问 iCloud。每个主号独立保存自己的配置；关闭开关后该主号恢复直连。

落地和前置代理均支持以下写法（未写协议时按 HTTP 处理）：

```text
hostname:port
hostname:port:username:password
username:password@hostname:port
hostname:port@username:password
http://username:password@hostname:port
https://username:password@hostname:port
socks5://username:password@hostname:port
```

代理凭据加密保存，管理接口只返回是否已配置。编辑时地址留空保留已保存的值；可勾选清除已有前置代理。连通检查失败时会提示前置或落地代理的认证、连接、TLS 等故障环节，方便区分代理协议和出口问题。每个主号独立维护 Apple 连接池，即使代理地址相同也不会混用连接；关闭代理后直连。代理发生变化后，该主号后续登录、收件和保活会使用新的连接链路。邮箱池长等待取码优先查询最新有效验证码，减少历史邮件记录读取。

## QQ 交流群

交流使用体验、反馈问题、功能建议、部署经验和开源协作：**1105888476** · [点击加入 QQ 群](https://qm.qq.com/q/6KWBNKKDEk)

## 最近更新

**v0.2.11：主号独立代理与高并发取码优化。**

- 每个主号可单独开启 HTTP、HTTPS 或 SOCKS5 代理，支持有认证的两级链路；配置加密保存，提供连通检查。
- Apple 登录、连接保活、隐私邮箱管理、IMAP 收件、IDLE 监听和自动登录接码按所属主号的代理设置联网。
- 切换代理时更新后续连接与监听，旧验证流程和等待中的自动登录识别代理变更；各主号独立维护 Apple HTTP 连接池。
- 邮箱池按时间从数据库直接读取最新有效验证码，减少并发请求中重复加载历史邮件的开销。

完整的功能变化、修复和升级说明见 [CHANGELOG.md](CHANGELOG.md)。


## 友情开源项目

- **[auto-team](https://github.com/DaiXuYQ/auto-team)**：一个面向 Team 空间和 Free 账号池的轻量管理台，适合需要统一维护 Team、账号、自动席位轮转和 Sub2API 数据的个人开发者与小型团队。

## Docker 部署

需要 **Docker Engine、Docker Compose v2 和 Git**，建议至少 **2 核 / 4 GB 内存**，保持单个应用实例。预构建镜像支持 **AMD64 / ARM64**；使用隐藏邮箱功能前，请准备具备该功能的 Apple 账户。

### 1. 拉取并启动

```sh
git clone --depth 1 https://github.com/t508708/icloud-mail.git
cd icloud-mail
cp -n .env.example .env

docker compose pull
docker compose up -d --wait
docker compose ps
```

默认拉取 `ghcr.io/t508708/icloud-mail:latest` 和配套 PostgreSQL 镜像，无需本地编译。`latest` 跟随主分支镜像发布，正式版本与离线包见 [Releases](https://github.com/t508708/icloud-mail/releases)。

### 2. 打开控制面板

使用示例配置时，后台地址为 **http://127.0.0.1:8788/admin/**，用户名为 `admin`。查看首次生成的密码：

```sh
docker compose exec -T icloud-api cat /app/keys/admin-password
```

如果部署在远程服务器，在自己的电脑建立 SSH 隧道后访问上面的地址：

```sh
ssh -N -L 127.0.0.1:8788:127.0.0.1:8788 USER@SERVER
```

需要公网域名、HTTPS、Cloudflare 或宝塔部署时，参阅 [公网部署指南](docs/PUBLIC.md)。

### 3. 接入自己的邮箱

在面板中 **添加主号 → 配置收件 → 连接 Apple → 同步地址**，然后复制取件链接，或选择地址加入邮箱池。自动创建与自动保活按需开启。

直连 iCloud IMAP 收件使用 **Apple App 专用密码**，与管理隐藏邮箱、重新登录 Apple 时使用的账户密码分开配置。

### 更新与源码构建

更新前备份数据库、密钥和邮件归档，保留 `.env` 与已有数据卷，再执行：

```sh
git pull --ff-only
docker compose pull
docker compose up -d --wait
```

需要自行构建时使用：

```sh
docker compose -f compose.yaml -f compose.build.yaml up -d --build --wait
```

备份、迁移、离线安装和固定版本部署详见 [安装与升级](DELIVERY.md)。

## API 接入

将面板中对应邮箱的 API Key 交给自己的程序，即可读取验证码：

```sh
curl -H 'Authorization: Bearer MAILBOX_API_KEY' \
  https://HOST/api/v1/otp
```

示例配置默认返回最新验证码；也可直接使用面板生成的取件链接。`root+tag@domain` 仅在对应根地址已登记时归入根邮箱，完整地址精确匹配优先，域名保持不变。

项目对接使用 [邮箱池与 API](docs/POOL.md)；完整请求定义见 [OpenAPI](docs/openapi.yaml)。部署后的 `/docs/` 也提供对应服务版本的接入说明。

## 自动创建能跑多少

以下为**每个主号滚动一小时的本地自动尝试上限**，不是 Apple 官方配额或成功数量承诺。

| 连接状态 | 自动计划 |
| --- | --- |
| 新旧通道均正常 | 新通道 **19 次** + 旧通道 **4 次**，合计 **23 次 / 小时**。 |
| 新通道失效、旧通道与目录正常 | 降级为旧通道 **5 次 / 小时**，新通道重新登录后恢复双通道规则。 |
| 旧通道目录校验失效 | 保留计划，暂停创建，恢复所需连接后继续。 |

**为什么不跑满 20 + 5？** 已有观察中，新旧通道常在约 20 次和 5 次附近触限，因此双通道计划各留一次余量，并分散执行时间，减少滚动窗口重叠或其他客户端操作导致的触顶等待。Apple 明确限流时遵守对应通道的冷却与更长 `Retry-After`。

失败也可能消耗尝试额度。手动创建与探测不受本地自动额度限制，仍受 Apple 实际限制；更多规则见 [自动创建与通道恢复](docs/AUTO_CREATION_CHANNELS.md)。

## 使用前了解

- **新通道仍可能在约 8 小时后失效。** 当前续期尚未证明解决这一现象。可选自动保活会在需要时重新登录，但仍取决于密码、接码服务和 Apple 账户状态，详见 [自动保活服务](docs/AUTO_LOGIN.md)。
- **取码速度受投递和上游连接影响。** 按需增量读取、连接复用与批量处理减少重复工作，实际耗时还取决于邮件是否已送达及服务器到上游的网络。
- **登录与收件是两类连接。** Apple 管理连接正常，不代表 IMAP 收件凭据也有效；两者可以分别维护。
- **妥善保管凭据与备份。** 登录密码和接码链接加密保存、不回显，服务运行时可解密使用。迁移时需要保留数据库和原密钥。
- **默认直连 Apple 服务。** 邮件接收使用 Apple 公开支持的标准 IMAP；隐藏邮箱管理适配 Apple 官网使用的接口，其行为可能随官网调整、账户区域及账户状态变化。本项目为独立维护的自托管工具，与 Apple 无隶属或背书关系。

## 文档

| 想了解什么 | 入口 |
| --- | --- |
| 安装、更新、备份与迁移 | [安装与升级](DELIVERY.md) · [公网与 HTTPS](docs/PUBLIC.md) |
| 邮箱池和程序接入 | [Pool API](docs/POOL.md) · [OpenAPI](docs/openapi.yaml) |
| 创建计划与登录恢复 | [自动创建](docs/AUTO_CREATION_CHANNELS.md) · [自动保活](docs/AUTO_LOGIN.md) |
| 收件与配置细节 | [邮件接收](docs/MAIL-RECEPTION.md) · [配置参考](docs/REFERENCE.md) |

## 致谢

- **[LINUX DO](https://linux.do)** — 社区支持与灵感

## 来源与许可

MIT 许可文本见 [LICENSE](LICENSE)；适用范围、上游来源与第三方许可见 [NOTICE.md](NOTICE.md)。

<p align="right">
  <sub>感谢原作者 <a href="https://github.com/mangobubu">mangobubu</a>。本项目基于 <a href="https://github.com/mangobubu/icloud-api">icloud-api</a> 持续开发，保留上游来源与 Git 历史。</sub>
</p>
<p align="right">
  <sub>不想折腾嫌麻烦，想直接到手即用的大白菜隐藏邮箱： <a href="https://qm.qq.com/q/z7TABWukrm">点击加入QQ群</a></sub>
</p>
