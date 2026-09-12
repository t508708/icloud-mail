# 公网部署

本机项目位于 `/opt/icloud-mail`，Web/API 域名为 `icloud-us.gooelv.com`，源站 IPv4 为 `23.251.34.11`。

## DNS

Cloudflare 的 `gooelv.com` 区域已配置：

| 类型 | 名称 | 内容 | 代理状态 |
| --- | --- | --- | --- |
| A | icloud-us | 23.251.34.11 | 已代理（橙云），Web/API 主入口 |
| A | imap-icloud.us | 23.251.34.11 | 仅 DNS（灰云），IMAPS 入口 |
| A | icloud.us | 23.251.34.11 | 原域名，橙云证书尚待 ACM |

Web 主入口使用 Cloudflare 已激活的免费 Universal SSL（`*.gooelv.com`）。普通 Cloudflare 代理支持 Web/API；IMAPS 1993 使用独立灰云域名，该名称只保留 A 记录，本机没有配置公网 IPv6 入口。

原泛解析 `*.us.gooelv.com` 和 `icloud.us.gooelv.com` 保留。2026-09-12 实测原多级域名在 Cloudflare 边缘报 TLS handshake failure；高级证书签发接口返回 1450，提示此区域需要 Advanced Certificate Manager。账号虽有既有高级证书，新增额度仍为 0。本次采用一级域名免费入口，未创建任何付费订阅。

## 入口

- Web 管理：`https://icloud-us.gooelv.com/admin/`。本机使用 `ICLOUD_API_ADMIN_PATH=/admin`，现有持久化 `keys/admin-path` 也已迁移为 `/admin/`。用户名 `admin`，密码见 `.local/access.json` 或 `docker compose exec -T icloud-api cat /app/keys/admin-password`。
- API：`https://icloud-us.gooelv.com/api/v1/pool/claim`，使用项目 Bearer Key。完整接口见 [邮箱池说明](POOL.md)。
- OAuth：`https://icloud-us.gooelv.com/oauth2/v2.0/token`。
- IMAPS：`imap-icloud.us.gooelv.com:1993`，隐式 TLS，支持密码及 XOAUTH2。公网 TCP 1993 已加入 firewalld 的运行和持久配置。
- `/` 保持上游公开文档入口，管理端使用固定 `/admin/` 入口。

首次接入仍需在 Web 主号管理中添加 Apple 账号、App 专用密码并完成登录，然后配置入池和定时创建。

## 服务与证书

`.env` 通过 `COMPOSE_FILE=compose.yaml:compose.public.yaml` 加载证书目录挂载；所有正常的 `docker compose` 命令都会使用该配置。HTTP 应用保持 `127.0.0.1:8788`，IMAPS 应用保持 `127.0.0.1:1993`。受信代理配置为当前 Docker 网桥网关 `172.22.0.1/32`，重建网络后需核对该地址。

本机 `.env` 的公网设置如下，其余数据库和构建设置保留：

```dotenv
COMPOSE_FILE=compose.yaml:compose.public.yaml
ICLOUD_API_ADMIN_PATH=/admin
ICLOUD_API_PUBLIC_IMAP_SERVER_NAME=imap-icloud.us.gooelv.com
ICLOUD_API_PUBLIC_IMAP_TLS_CERT_FILE=/app/public-tls/fullchain.pem
ICLOUD_API_PUBLIC_IMAP_TLS_KEY_FILE=/app/public-tls/privkey.pem
ICLOUD_API_COOKIE_SECURE=true
ICLOUD_API_TRUSTED_PROXIES=172.22.0.1/32
```

部署文件：

| 项目内文件 | 本机生效位置 |
| --- | --- |
| `deploy/nginx/icloud.us.gooelv.com.conf` | `/www/server/panel/vhost/nginx/icloud.us.gooelv.com.conf` |
| `deploy/nginx/icloud-mail-imaps.conf` | `/www/server/panel/vhost/nginx/tcp/icloud-mail-imaps.conf` |
| `deploy/nginx/cloudflare-realip.conf` | `/www/server/panel/vhost/nginx/extension/icloud-mail/cloudflare-realip.conf` |
| `scripts/deploy-public-certificate.sh` | 由 `/etc/letsencrypt/renewal-hooks/deploy/60-icloud-mail` 调用 |
| `deploy/certbot/60-icloud-mail` | `/etc/letsencrypt/renewal-hooks/deploy/60-icloud-mail`，权限 0755 |

Nginx 负责 HTTP 到主入口 HTTPS 的 308 跳转和 HTTPS 反向代理，仅信任 Cloudflare 官方 IP 范围传入的 `CF-Connecting-IP`，再向应用传递客户端 IP。应用开启 Secure Cookie；此站点关闭访问日志，错误日志仅记 crit，避免 OTP 查询参数进入代理请求日志。IMAPS 通过 Nginx stream 转发至应用并由应用完成 TLS 握手。Cloudflare 区域级 SSL 模式沿用现有的 Full。

源站 Let's Encrypt 证书在 `/etc/letsencrypt/live/icloud.us.gooelv.com/`，同时覆盖新 Web 域名、原 Web 域名和 IMAPS 域名，HTTP-01 验证目录为 `/var/www/icloud-mail-acme`。已启用的 `certbot-renew.timer` 负责定期续期。部署 hook 校验三个域名覆盖，将证书复制至 `.local/tls`（目录 root:10001 / 0750，文件 0640），重建应用以加载新 IMAPS 证书，并重载 Nginx；证书更新时应用会短暂停顿。Cloudflare 边缘的 Universal SSL 由 Cloudflare 自动续期。

手动重新安装当前证书：

```sh
cd /opt/icloud-mail
sh scripts/deploy-public-certificate.sh
docker compose ps
```

## 验证与恢复

缓存边界：仅 `/admin/assets/` 保留应用返回的静态资源缓存头，并允许代理缓存响应；其余 HTML、API 和 `/admin/` 路由继续由统一代理规则覆盖为 `Cache-Control: no-store`。Nginx 不启用全站缓存。

```sh
curl -sSI 'https://icloud-us.gooelv.com/admin/assets/<asset>.js'
curl -sSI https://icloud-us.gooelv.com/admin/
curl -sS -D - -o /dev/null https://icloud-us.gooelv.com/admin/api/v1/auth/session
```

将 `<asset>` 替换为当前 HTML 引用的文件名。`/admin/` 和 API 响应应继续为 `Cache-Control: no-store`，且 `CF-Cache-Status` 不是 `HIT`；已有静态文件应返回 `public, max-age=31536000, immutable`，缺失文件仍返回 404 且不采用长期缓存。

```sh
dig +short icloud-us.gooelv.com A
dig +short imap-icloud.us.gooelv.com A
curl --fail https://icloud-us.gooelv.com/healthz
openssl s_client -connect imap-icloud.us.gooelv.com:1993 \
  -servername imap-icloud.us.gooelv.com \
  -verify_hostname imap-icloud.us.gooelv.com -verify_return_error
```

Web 域名应返回 Cloudflare 地址，IMAPS 域名应返回 `23.251.34.11`。以下命令单独验证源站 HTTPS 和证书：

```sh
curl --fail --resolve icloud-us.gooelv.com:443:23.251.34.11 \
  https://icloud-us.gooelv.com/healthz
```

部署验证包含 Cloudflare 公网 TLS 握手、通过普通域名的 HTTPS 浏览器登录、邮箱池五个页面、Secure Cookie、空库存 API 响应，以及独立 IMAPS 域名的证书验证与 CAPABILITY。

本次三域名正式证书已成功签发，部署 hook 已实际执行并完成应用健康检查。扩展证书后的 staging 续期演练返回 ACME 端的 `Service busy` 和 `authorization must be pending`；当前正式证书有效至 2026-12-11，自动续期定时器已启用，后续可再次运行 dry-run 检查 staging 状态。

`node scripts/cloudflare-public.mjs status` 检查项目 DNS 和边缘证书，令牌从 `.local/cloudflare-token`（0600）读取。`ensure-web-dns` 和 `ensure-imap-dns` 可重复执行，遇到不同配置的既有记录会停止；操作前状态和返回结果存入 `.local/cloudflare/`。`order-edge-cert` 仅用于已开通 ACM 后为原 `*.us.gooelv.com` 签发高级证书，脚本没有订阅购买操作。首次运行所需权限为指定 `gooelv.com` 区域的 Zone Read、DNS Edit、SSL and Certificates Edit；只查询状态时读取权限即可。

`sh scripts/backup-local.sh` 同时备份数据库、keys、邮件归档、访问凭据、`.env` 和 `.local/tls`。恢复时将 `deployment.env` 放回 `.env`，将 `public-tls.tar` 解至 `.local/tls` 并保留属主和权限，再按主 README 恢复数据库与数据卷。过期的 TLS 备份需先重新签发并安装证书。Nginx 配置保存在 Git 中；迁移服务器时另需迁移 `/etc/letsencrypt`（包含续期账户与配置）或重新申请证书，并更新源站 IP、DNS 和代理信任地址。
