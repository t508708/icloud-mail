# 安装与交付

公开仓库：<https://github.com/t508708/icloud-mail>。当前发布为 **v0.2.1**，提供源码和 Linux amd64 预构建镜像包，变化见 [更新日志](CHANGELOG.md)。源码包含 Web 控制面板、Go 服务、测试和 Docker 构建配置，不含账号、邮件、密码、证书或现有服务器的数据。来源与许可见 [NOTICE.md](NOTICE.md)。

## GHCR latest 安装

默认部署从 GHCR 拉取当前镜像，支持 linux/amd64 和 linux/arm64。要求 Docker Engine、Docker Compose v2 和 Git，建议至少 2 核、4 GB 内存；磁盘空间按邮件归档量预留。

```sh
git clone --depth 1 https://github.com/t508708/icloud-mail.git
cd icloud-mail
cp -n .env.example .env
docker compose pull
docker compose up -d --wait
docker compose ps
curl -fsS http://127.0.0.1:8788/healthz
```

基础编排默认使用 `ghcr.io/t508708/icloud-mail:latest` 与 `ghcr.io/t508708/icloud-mail-postgres:latest`。可在 `.env` 中用 `ICLOUD_API_IMAGE` 和 `ICLOUD_API_POSTGRES_IMAGE` 覆盖镜像或固定 digest；应用运行仍需访问 Apple/上游 IMAP。

## 源码构建安装

源码构建使用独立覆盖文件，不改变默认的 GHCR 拉取方式：

```sh
git clone https://github.com/t508708/icloud-mail.git
cd icloud-mail
cp -n .env.example .env
docker compose -f compose.yaml -f compose.build.yaml up -d --build --wait
docker compose -f compose.yaml -f compose.build.yaml ps
curl -fsS http://127.0.0.1:8788/healthz
```

构建阶段需要访问 Docker 基础镜像、Go 模块和 npm 依赖源。复制示例配置后，HTTP 发布到 `127.0.0.1:8788`，IMAPS 发布到 `127.0.0.1:1993`。前端 JS/CSS 随镜像本地提供；运行时仍需访问 Apple/iCloud 或配置的上游 IMAP 服务。

## 首次登录

首次空安装自动生成管理员密码、OAuth Token、主密钥、管理路径和 IMAPS 自签证书，保存在 `icloud_api_keys` 卷。获取管理员密码和路径：

```sh
docker compose exec -T icloud-api cat /app/keys/admin-password
docker compose exec -T icloud-api cat /app/keys/admin-path
```

用户名为 `admin`。使用 `.env.example` 时管理入口为 `http://127.0.0.1:8788/admin/`。首次接入在主号管理中添加 Apple 账号并完成连接；直连 iCloud IMAP 使用 Apple App 专用密码，第三方 IMAP 使用该服务自己的凭据。

云服务器可在自己的电脑建立 SSH 隧道：

```sh
ssh -N -L 127.0.0.1:8788:127.0.0.1:8788 USER@SERVER
```

然后用本地浏览器访问管理入口。公网 HTTPS、域名与证书见 [公网部署](docs/PUBLIC.md)；宝塔多文件编排的兼容方式也在该文档中。

默认按需共享同步并返回最新 OTP；共享取件容量为 2，每 2 秒恢复 1 次，等待读取与普通读取分开，被拦截请求不延长冷却。自动创建功能需主动开启；Apple 的限流、条款和会话状态以实际响应为准，长时会话恢复仍需持续验证。保持单个应用实例。

## 数据备份与更新

数据库、keys、installation state 与 mail archive 应形成同一个备份点，完整备份流程见 [配置与兼容参考](docs/REFERENCE.md#v1--v2-兼容升级) 和 `scripts/backup-local.sh`。更新时保留 `.env` 和已有 Docker volumes，不执行带 `-v` 的清理：

```sh
sh scripts/backup-local.sh
git pull --ff-only
docker compose pull
docker compose up -d --wait
```

使用宝塔单文件入口的安装，在更新后、部署前运行 `bash scripts/render-baota-compose.sh`，将更新后的基础编排重新合并。不要将正在使用的数据库、密钥、证书或备份提交到 GitHub。

## 离线与固定版本

无 GHCR 网络或需要可复现版本时，从 [v0.2.1 Release](https://github.com/t508708/icloud-mail/releases/tag/v0.2.1) 下载 `icloud-mail-v0.2.1-linux-amd64.zip` 和 `SHA256SUMS`。该历史镜像包仅适用于 Linux x86_64 / amd64：

```sh
sha256sum --ignore-missing -c SHA256SUMS
unzip icloud-mail-v0.2.1-linux-amd64.zip
cd icloud-mail-v0.2.1
cp -n .env.example .env
docker load -i images/linux-amd64.tar.gz
docker compose -f compose.yaml -f compose.offline.yaml up -d --no-build --pull never --wait
```

包内包含 `icloud-api:v0.2.1` 与 `icloud-api-postgres:v0.2.1`，`RELEASE.json` 记录源码提交、架构和镜像 ID，包内 `SHA256SUMS` 可再次校验各文件。后续命令继续带上 `-f compose.yaml -f compose.offline.yaml`。历史 `handoff-2026.09.14` 包继续使用其自带的编排和镜像，不混用不同版本的离线文件。

已有镜像包升级时，先备份，再下载并校验新包、导入新镜像、更新项目中的发布文件。保留原 `.env`、`COMPOSE_PROJECT_NAME` 和数据卷；沿用原项目目录，避免更换项目名后被识别为新安装：

```sh
docker load -i images/linux-amd64.tar.gz
docker compose -f compose.yaml -f compose.offline.yaml up -d --no-build --pull never --wait
```

## 维护者打包

从发布标签的干净源码构建 `runtime` 和 `postgres-runtime` 两个 Docker target，分别标记为 `icloud-api:TAG`、`icloud-api-postgres:TAG`，设置 OCI `version` 为标签、`revision` 为完整源码提交。运行 `bash scripts/package-release.sh TAG` 生成源码 ZIP、含镜像 ZIP 和外部 `SHA256SUMS`。脚本核对版本/提交/架构，只归档标签中的文件；不使用运行容器制作镜像。
