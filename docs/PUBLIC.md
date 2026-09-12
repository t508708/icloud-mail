# 公网部署

本机项目位于 `/opt/icloud-mail`，域名为 `icloud.us.gooelv.com`，源站 IPv4 为 `23.251.34.11`。

## DNS

在 Cloudflare 的 `gooelv.com` 区域新增独立记录，原泛解析可以保留：

| 类型 | 名称 | 内容 | 代理状态 |
| --- | --- | --- | --- |
| A | icloud.us | 23.251.34.11 | 仅 DNS（灰云） |

2026-09-12 部署时，泛解析经 Cloudflare 代理已成功回源 HTTP，但该多级子域名在 Cloudflare 边缘返回 TLS handshake failure。源站已取得 Let's Encrypt 证书，使用独立灰云记录即可直连源站；IMAPS 的 1993 端口也需要直连。该独立名称应只保留上面的 A 记录，本机没有配置公网 IPv6 入口。

## 入口

- Web 管理：`https://icloud.us.gooelv.com` 加 `.local/access.json` 中的管理路径。用户名 `admin`，密码见该文件或 `docker compose exec -T icloud-api cat /app/keys/admin-password`。
- API：`https://icloud.us.gooelv.com/api/v1/pool/claim`，使用项目 Bearer Key。完整接口见 [邮箱池说明](POOL.md)。
- OAuth：`https://icloud.us.gooelv.com/oauth2/v2.0/token`。
- IMAPS：`icloud.us.gooelv.com:1993`，隐式 TLS，支持密码及 XOAUTH2。公网 TCP 1993 已加入 firewalld 的运行和持久配置。
- `/` 保持上游公开文档入口，管理端使用独立随机路径。

首次接入仍需在 Web 主号管理中添加 Apple 账号、App 专用密码并完成登录，然后配置入池和定时创建。

## 服务与证书

`.env` 通过 `COMPOSE_FILE=compose.yaml:compose.public.yaml` 加载证书目录挂载；所有正常的 `docker compose` 命令都会使用该配置。HTTP 应用保持 `127.0.0.1:8788`，IMAPS 应用保持 `127.0.0.1:1993`。受信代理配置为当前 Docker 网桥网关 `172.22.0.1/32`，重建网络后需核对该地址。

本机 `.env` 的公网设置如下，其余数据库和构建设置保留：

```dotenv
COMPOSE_FILE=compose.yaml:compose.public.yaml
ICLOUD_API_PUBLIC_IMAP_SERVER_NAME=icloud.us.gooelv.com
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
| `scripts/deploy-public-certificate.sh` | 由 `/etc/letsencrypt/renewal-hooks/deploy/60-icloud-mail` 调用 |
| `deploy/certbot/60-icloud-mail` | `/etc/letsencrypt/renewal-hooks/deploy/60-icloud-mail`，权限 0755 |

Nginx 负责 HTTP 到 HTTPS 的 308 跳转和 HTTPS 反向代理。应用开启 Secure Cookie；此站点关闭访问日志，错误日志仅记 crit，避免 OTP 查询参数进入代理请求日志。IMAPS 通过 Nginx stream 转发至应用并由应用完成 TLS 握手。

Let's Encrypt 证书在 `/etc/letsencrypt/live/icloud.us.gooelv.com/`，HTTP-01 验证目录为 `/var/www/icloud-mail-acme`。已启用的 `certbot-renew.timer` 负责定期续期。部署 hook 将证书复制至 `.local/tls`（目录 root:10001 / 0750，文件 0640），重建应用以加载新 IMAPS 证书，并重载 Nginx；证书更新时应用会短暂停顿。

手动重新安装当前证书：

```sh
cd /opt/icloud-mail
sh scripts/deploy-public-certificate.sh
docker compose ps
```

## 验证与恢复

```sh
dig +short icloud.us.gooelv.com A
curl --fail https://icloud.us.gooelv.com/healthz
openssl s_client -connect icloud.us.gooelv.com:1993 \
  -servername icloud.us.gooelv.com \
  -verify_hostname icloud.us.gooelv.com -verify_return_error
```

DNS 生效前可以用下面的命令单独验证源站 HTTPS 和证书，它不会验证 Cloudflare 边缘：

```sh
curl --fail --resolve icloud.us.gooelv.com:443:23.251.34.11 \
  https://icloud.us.gooelv.com/healthz
```

部署验证已通过：源站证书链和主机名校验、HTTPS 浏览器登录、邮箱池五个页面、Secure Cookie、空库存 API 响应、IMAPS CAPABILITY，以及 `certbot renew --cert-name icloud.us.gooelv.com --dry-run`。首次验证通过指定源站 IP 完成，Cloudflare 的独立灰云记录需要在 DNS 控制台配置后再检查普通域名访问。

`sh scripts/backup-local.sh` 同时备份数据库、keys、邮件归档、访问凭据、`.env` 和 `.local/tls`。恢复时将 `deployment.env` 放回 `.env`，将 `public-tls.tar` 解至 `.local/tls` 并保留属主和权限，再按主 README 恢复数据库与数据卷。过期的 TLS 备份需先重新签发并安装证书。Nginx 配置保存在 Git 中；迁移服务器时另需迁移 `/etc/letsencrypt`（包含续期账户与配置）或重新申请证书，并更新源站 IP、DNS 和代理信任地址。
