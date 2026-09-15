# 使用自己的域名部署

以下以 `mail.example.com` 和 HTTP 回环端口 `8788` 为例。先按 [交付说明](../DELIVERY.md) 启动服务并确认本机健康检查成功。

## Web/API 与控制面板

1. 将自己的域名解析到服务器公网 IP。
2. 在宝塔、Nginx 或现有反向代理中创建该域名的站点，申请覆盖该域名的 Let's Encrypt 证书。
3. HTTPS 反向代理指向 `http://127.0.0.1:8788`。宝塔中的 SSL 证书用于 Web 代理；仅使用 HTTP API 时无需配置应用内 IMAPS 证书。
4. 在 `.env` 设置 `ICLOUD_API_COOKIE_SECURE=true`，重建应用容器使设置生效。保持单实例。

源码安装：

```sh
docker compose up -d --no-deps --wait icloud-api
```

镜像包安装：

```sh
docker compose -f compose.yaml -f compose.offline.yaml up -d --no-build --pull never --no-deps --wait icloud-api
```

Nginx 的 HTTPS 站点至少配置以下代理内容。证书路径使用该站点实际签发的位置：

```nginx
location / {
    proxy_pass http://127.0.0.1:8788;
    proxy_set_header Host $http_host;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header Origin $http_origin;
    proxy_connect_timeout 5s;
    proxy_read_timeout 90s;
    proxy_send_timeout 90s;
    proxy_buffering off;
    access_log off;
}
```

保留 `Host` 和外部协议，控制面板复制的取件链接才会使用自己的 HTTPS 域名。取件及管理 API 设置为不缓存；访问日志省略含 token 的查询字符串。`ICLOUD_API_TRUSTED_PROXIES` 应填写应用实际接收到的代理 IP/CIDR，Docker 桥接地址按目标服务器检查。

访问 `https://mail.example.com/admin/`，使用自己安装生成的管理员密码登录。证书续期由所用面板或 ACME 客户端管理。

## Cloudflare

- Web/API 域名可启用橙云，SSL/TLS 选择 **Full (strict)**，源站安装有效域名证书。
- 使用已被边缘证书覆盖的域名。标准 Universal SSL 通常覆盖区域根域名及其一级子域名；多级子域名需检查实际证书覆盖范围。
- 管理页及 `/api/` 设置缓存绕过；静态资源可沿用应用缓存头。
- 普通橙云代理用于 HTTP/HTTPS。公开 IMAPS 使用独立灰云域名或支持该协议的 TCP 代理。

## 可选：公开只读 IMAPS

仅通过 HTTPS 取码时可跳过此节。公开 IMAPS 时：

1. 将 `imap.example.com` 灰云解析到服务器。
2. 准备覆盖该名称的完整证书链与私钥，放入项目 `.local/tls/fullchain.pem`、`.local/tls/privkey.pem`，让容器用户 UID/GID `10001` 可读。
3. 在 `.env` 配置下面三项：

```dotenv
ICLOUD_API_PUBLIC_IMAP_SERVER_NAME=imap.example.com
ICLOUD_API_PUBLIC_IMAP_TLS_CERT_FILE=/app/public-tls/fullchain.pem
ICLOUD_API_PUBLIC_IMAP_TLS_KEY_FILE=/app/public-tls/privkey.pem
```

4. 启动时增加 `-f compose.public.yaml` 挂载证书目录；镜像包安装同时保留 `-f compose.offline.yaml`。
5. 用 Nginx stream 等 TCP 透传代理将公网 `993` 转发到 `127.0.0.1:1993`，放通相应防火墙端口；TLS 握手由应用完成。
6. 证书续期后同步更新挂载文件，并重建应用以加载新证书。

```sh
openssl s_client -connect imap.example.com:993 \
  -servername imap.example.com -verify_hostname imap.example.com -verify_return_error
```

应用首次生成的自签 IMAPS 证书只用于本地试用。每个新安装使用自己的域名、证书和数据卷。

## 宝塔容器编排的单文件入口

部分宝塔版本把 Docker 返回的多个配置路径当成一个文件名，出现 `[.../compose.yaml,.../compose.public.yaml] 文件不存在`。使用单文件入口可解决这个兼容问题：

```sh
# 默认合并基础配置和公网证书挂载，保留环境变量占位符。
bash scripts/render-baota-compose.sh
# 镜像包安装则使用下面这条生成命令：
# bash scripts/render-baota-compose.sh compose.yaml compose.offline.yaml compose.public.yaml
```

在 `.env` 中将 `COMPOSE_FILE` 设置为 `compose.baota.yaml`，保留原来的 `COMPOSE_PROJECT_NAME`。初次切换需重建全部项目容器，Docker 才会更新宝塔读取的配置路径；会有短暂服务中断：

```sh
docker compose stop icloud-api
docker compose up -d --force-recreate --no-build --pull never --wait --wait-timeout 120
docker compose ls
```

操作前确认本地镜像标签指向要运行的版本。重建沿用原项目数据卷；`docker compose ls` 中本项目的 `ConfigFiles` 应只显示 `compose.baota.yaml` 的绝对路径。然后刷新宝塔容器编排页。

后续通过普通 `docker compose` 命令管理，避免再传多个 `-f`。更新基础编排或覆盖文件后，先重新运行生成命令，再部署；生成会覆盖在宝塔直接编辑的单文件内容。`compose.baota.yaml` 是本地生成文件，已排除版本控制。
