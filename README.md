# iCloud Mail

自托管的 **iCloud 隐藏邮箱管理、邮箱池与 API 取码服务**，带本地化 Web 控制面板。

**当前版本：v0.2.1** · [下载与发布说明](https://github.com/t508708/icloud-mail/releases/tag/v0.2.1) · [更新日志](CHANGELOG.md) · [安装文档](DELIVERY.md)

## 核心功能

| 功能 | 说明 |
| --- | --- |
| Web 控制面板 | 苹果风格界面、深浅色主题；JS/CSS 本地加载 |
| 隐藏邮箱管理 | 多主号、目录同步、手动批量创建、自动计划、批量删除 |
| 邮箱池 | 项目 Key、自动领取、确认使用、释放、续期与凭据轮换 |
| API 取码 | 单邮箱链接、Bearer API、Pool lease 取码；严格支持 `+tag` 归入已登记根邮箱 |
| 邮件接收 | 按需增量收件、同主号连接复用、MIME 归档、只读 IMAPS |
| 独立凭据 | 每邮箱 API Key、IMAP 密码及 OAuth 凭据，由本网关签发 |

## v0.2.1 更新重点

- **取件限流**：共享取件容量为 2，每 2 秒恢复 1 次；等待读取与普通读取分开，被拦截的请求不消耗额度，也不延期冷却。
- **自动创建**：每主号 19+4 错峰、独立通道冷却默认 1 小时，并尊重 Apple 返回的更长等待时间；手动探测保持独立。

从初版 `v0.1.0` 起的完整新增、调整、修复与验证记录见 [更新日志](CHANGELOG.md)。

## Docker 部署

需要 Docker Engine、Docker Compose v2、`curl` 和 `unzip`。预构建包适用于 **Linux x86_64 / amd64**，包含应用与 PostgreSQL 镜像，建议至少 2 核、4 GB 内存。

**1. 下载并校验**

```sh
curl -fLO https://github.com/t508708/icloud-mail/releases/download/v0.2.1/icloud-mail-v0.2.1-linux-amd64.zip
curl -fLO https://github.com/t508708/icloud-mail/releases/download/v0.2.1/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
```

确认校验结果为 `OK` 后继续。

**2. 导入镜像并启动**

```sh
unzip icloud-mail-v0.2.1-linux-amd64.zip
cd icloud-mail-v0.2.1
cp -n .env.example .env
docker load -i images/linux-amd64.tar.gz
docker compose -f compose.yaml -f compose.offline.yaml up -d --no-build --pull never --wait
docker compose -f compose.yaml -f compose.offline.yaml ps
```

**3. 登录后台**

默认地址：**http://127.0.0.1:8788/admin/**，用户名 `admin`。查看首次生成的密码：

```sh
docker compose -f compose.yaml -f compose.offline.yaml exec -T icloud-api cat /app/keys/admin-password
```

远程服务器可在自己的电脑运行以下命令，再打开上面的本地地址：

```sh
ssh -N -L 8788:127.0.0.1:8788 USER@SERVER
```

公网域名、HTTPS 与宝塔配置见 [公网部署](docs/PUBLIC.md)。镜像已包含前端资源，运行时仍需连接 Apple 或配置的上游 IMAP。

### 从源码构建

希望自行构建时使用：

```sh
git clone --branch v0.2.1 https://github.com/t508708/icloud-mail.git
cd icloud-mail
cp -n .env.example .env
docker compose up -d --build --wait
```

两种方式使用同一控制面板。登录后添加主号、配置收件、连接 Apple 并同步地址，再复制取码链接或将邮箱加入池。直连 iCloud 收件使用 **App 专用密码**；Apple 登录用于地址管理。

升级前按 [安装文档](DELIVERY.md) 备份数据库、密钥与邮件归档，保留 `.env` 和数据卷。

## 文档

[安装与升级](DELIVERY.md) · [公网部署](docs/PUBLIC.md) · [邮箱池与 API](docs/POOL.md) · [OpenAPI](docs/openapi.yaml)

[配置与兼容说明](docs/REFERENCE.md) · [收件设计](docs/MAIL-RECEPTION.md) · [测试与实测记录](docs/RECEIVER-VALIDATION.md)

## QQ 群

项目交流与部署问题可加入 QQ 群 **1105888476**：[一键加群](https://qun.qq.com/#/handy-tool/join-group?groupcode=1105888476)。

## 来源与许可

本项目由 [t508708/icloud-mail](https://github.com/t508708/icloud-mail) 维护，基于 [mangobubu/icloud-api](https://github.com/mangobubu/icloud-api) 开发，保留来源与 Git 历史。

上游目前未提供通用软件许可证，本仓库未另行声明整个代码库的 MIT/GPL 授权。原作者、贡献者与依赖各自的权利保留，详见 [NOTICE.md](NOTICE.md)。本项目为非官方工具，与 Apple 无隶属关系。
