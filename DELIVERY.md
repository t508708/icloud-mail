# 安装与交付

公开仓库：<https://github.com/t508708/icloud-mail>。源码包含 Web 控制面板、Go 服务、测试和 Docker 构建配置，不含账号、邮件、密码、证书或现有服务器的数据。

## 源码构建安装

要求 Docker Engine、Docker Compose v2（支持所用 Compose 配置）。建议至少 2 核、4 GB 内存；磁盘空间按邮件归档量预留。

```sh
git clone https://github.com/t508708/icloud-mail.git
cd icloud-mail
cp -n .env.example .env
docker compose up -d --build --wait
docker compose ps
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

默认按需同步并返回最新 OTP，同一邮箱取件间隔为 3 秒。自动创建功能需主动开启；Apple 的限流、条款和会话状态以实际响应为准，长时会话恢复仍需持续验证。保持单个应用实例。

## 数据备份与更新

数据库、keys、installation state 与 mail archive 应形成同一个备份点，备份流程见 `README.md` 和 `scripts/backup-local.sh`。更新时保留 `.env` 和已有 Docker volumes，不执行带 `-v` 的清理。

```sh
sh scripts/backup-local.sh
git pull --ff-only
docker compose up -d --build --wait
```

使用宝塔单文件入口的安装，在更新后、部署前运行 `bash scripts/render-baota-compose.sh`，将更新后的基础编排重新合并。不要将正在使用的数据库、密钥、证书或备份提交到 GitHub。

## 可选：历史离线镜像交付包

首次 GitHub 发布只提供源码，不声明已有可下载的预构建镜像。仓库中的 `compose.offline.yaml` 是历史 `handoff-2026.09.14` 镜像包的配套文件，只有持有匹配交付包时才使用：

```sh
cp -n .env.example .env
docker load -i images/linux-amd64.tar.gz
docker compose -f compose.yaml -f compose.offline.yaml up -d --no-build --pull never --wait
```

`scripts/package-release.sh` 面向持有匹配本地 Git 标签及镜像的维护者；公共仓库不包含旧服务器的本地标签和交付数据。新用户请使用上面的源码构建流程。
