# 朋友安装说明

本交付对应源码标签 `handoff-2026.09.14`，运行基线为 `baseline-2026.09.14=af953a9`。交付包包含完整源码；完整版另含 `images/linux-amd64.tar.gz`，其中只有两张 Docker 镜像，不含数据库、密钥或业务数据。

## 离线镜像版（amd64）

要求 Docker Engine、Docker Compose v2。建议至少 2 核、4 GB 内存；磁盘空间应按邮件归档量预留。

```sh
cp -n .env.example .env
docker load -i images/linux-amd64.tar.gz
docker compose -f compose.yaml -f compose.offline.yaml up -d --no-build --pull never --wait
```

离线 Compose 固定使用镜像标签 `icloud-api:handoff-2026.09.14` 和 `icloud-api-postgres:handoff-2026.09.14`。运行时仍需联网访问 Apple/iCloud、IMAP 等上游；前端资源已本地化，免编译不等于完整离线邮件平台。目标机是 arm64 时使用源码版构建。

## 源码构建版

```sh
cp -n .env.example .env
docker compose up -d --build --wait
```

构建阶段需要访问 Docker 基础镜像、Go 模块和 npm 依赖源。Compose 默认将 HTTP 发布到 `127.0.0.1:8788`，IMAPS 发布到 `127.0.0.1:1993`；健康检查：

```sh
curl -fsS http://127.0.0.1:8788/healthz
```

## 首次登录

首次空安装会自动生成管理员密码、OAuth Token、主密钥、管理路径和 IMAPS 自签证书，并保存在 `icloud_api_keys` 卷。使用 `admin` 用户和以下命令取得密码、路径：

```sh
docker compose -f compose.yaml -f compose.offline.yaml exec -T icloud-api cat /app/keys/admin-password
docker compose -f compose.yaml -f compose.offline.yaml exec -T icloud-api cat /app/keys/admin-path
```

源码版去掉两个 `-f`；也可在 `.env` 设置 `COMPOSE_FILE=compose.yaml:compose.offline.yaml` 后统一使用 `docker compose`。浏览器打开 `http://127.0.0.1:8788` 加取得的管理路径。

首次使用需在管理界面自行添加 Apple 主号并完成登录，直连 iCloud 需要 App 专用密码；第三方 IMAP 或自定义邮箱需填写对应 IMAP 凭据。默认按需同步并返回最新 OTP，取件有 3 秒限流；自动创建功能需要主动开启。Apple 约 8 小时长时恢复流程仍在验证中，不作永久稳定承诺。

云主机可通过 SSH 隧道访问管理界面：

```sh
ssh -N -L 127.0.0.1:8788:127.0.0.1:8788 USER@SERVER
```

然后在本地浏览器访问 `http://127.0.0.1:8788/admin/`。公网 HTTPS、域名、证书和反向代理配置见 [公网部署](docs/PUBLIC.md)，需按自己的域名和服务器环境调整。

## 数据与更新

不要删除或更换现有 Docker volumes；更新镜像或源码时保持卷不变。数据库、keys、installation state 和 mail archive 必须一起备份，备份流程见 `README.md` 或 `scripts/backup-local.sh`。这些数据不包含在交付包中。`.git` 历史也不随交付；如需在新目录建立自己的仓库：

```sh
git init -b main
git add .
git commit -m 'Import handoff-2026.09.14'
```

提交前设置自己的 Git identity；先在 `.gitignore` 追加 `/images/`，镜像归档不加入源码 Git。包内 `RELEASE.json` 记录源码提交和镜像 ID；解压后、复制或修改文件前执行校验：

```sh
sha256sum -c SHA256SUMS
```

固定此版本后，其他机器可重建基线；应用限流、调度和账号锁按单进程协调，请保持一个应用实例。版本基线冻结现有功能和验证结果，Apple 长时会话表现仍以实际运行为准。
