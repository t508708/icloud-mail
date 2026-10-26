<div align="center">

**[简体中文](README.md) | English**

# iCloud Mail

**Manage iCloud Hide My Email addresses, receive mail, and retrieve verification codes through an API—all in one dashboard.**

Self-hosted · Multiple primary accounts · Mailbox pools · On-demand mail retrieval · Light and dark themes

**Current version: v0.2.9**

[Downloads and release notes](https://github.com/t508708/icloud-mail/releases) · [Docker deployment](#docker-deployment) · [API documentation](docs/POOL.md) · [Changelog](CHANGELOG.md)

</div>

For users who want to manage iCloud Hide My Email addresses centrally, automatically replenish their address inventory, or integrate mail retrieval and verification-code APIs into their own tools. Use the web dashboard for everyday tasks, or the mailbox pool API to acquire, use, and retire addresses.

## Features

| Capability | What it offers |
| --- | --- |
| **Manage multiple primary accounts in one dashboard** | An Apple-style interface with Liquid Glass effects and light/dark modes; JavaScript and CSS are served locally by the application. |
| **Bulk management of Hide My Email addresses** | Sync addresses, search and filter, select across pages, use drag selection, and create or delete addresses in batches. |
| **Automatic address replenishment** | Manual batch creation and scheduled **automatic creation plans** work together; the new and legacy channels operate according to their respective limits and status. |
| **Project mailbox pools** | Issue Pool Keys for different projects, with APIs to acquire addresses, confirm use, renew leases, release addresses, and retire them. |
| **Verification codes via links and API** | Each mailbox has its own retrieval link; mail addressed to `+tag` variants of a registered root address can be retrieved through the root mailbox. |
| **On-demand mail retrieval** | Visiting a retrieval link or calling the API triggers incremental reads. Connections are reused within the same primary account to reduce duplicate requests and downloads of historical message bodies. |
| **Separate credentials and connection recovery** | Each mailbox has its own rotatable API Key, IMAP password, and OAuth credentials. Optionally configure a two-factor authentication code retrieval URL to log in again automatically when a connection expires. |

**About credentials:** Separate IMAP/OAuth credentials are issued by this gateway for connecting to this service. Access to Apple's upstream mailbox still requires the corresponding primary account's mail retrieval credentials.

## QQ Community

Discuss your experience, report issues, suggest features, share deployment tips, and collaborate on the project: **1105888476** · [Join the QQ group](https://qm.qq.com/q/6KWBNKKDEk)

## Recent Updates

**v0.2.9: Prevent reused addresses and show accurate available inventory.**

- Previously used addresses stay excluded even after deletion and re-import under a new ID. Inventory lists, filtered totals, and claimable counts follow the same rules.
- Re-enabling a primary account keeps addresses with prior usage paused while preserving other mailbox states and project ownership.
- Confirm up to 50 acquired mailboxes in one request, reducing API calls for larger workloads.
- Includes complete-directory cleanup and fixes for long-pending addresses, with updated deployment and API documentation.

See [CHANGELOG.md](CHANGELOG.md) for full feature changes, fixes, and upgrade notes.

## Related Open-Source Project

- **[auto-team](https://github.com/DaiXuYQ/auto-team)**: A lightweight management dashboard for Team workspaces and Free account pools, for individual developers and small teams that need to manage Teams, accounts, automatic seat rotation, and Sub2API data centrally.

## Docker Deployment

Requires **Docker Engine, Docker Compose v2, and Git**. At least **2 CPU cores / 4 GB RAM** is recommended. Run a single application instance. Prebuilt images support **AMD64 / ARM64**. To use Hide My Email features, prepare an Apple account with access to that feature.

### 1. Clone and Start

```sh
git clone --depth 1 https://github.com/t508708/icloud-mail.git
cd icloud-mail
cp -n .env.example .env

docker compose pull
docker compose up -d --wait
docker compose ps
```

By default, this pulls `ghcr.io/t508708/icloud-mail:latest` and the accompanying PostgreSQL image; no local compilation is required. `latest` follows image releases from the main branch. Formal releases and offline packages are available under [Releases](https://github.com/t508708/icloud-mail/releases).

### 2. Open the Dashboard

With the example configuration, the dashboard is at **http://127.0.0.1:8788/admin/** and the username is `admin`. Retrieve the password generated on first startup:

```sh
docker compose exec -T icloud-api cat /app/keys/admin-password
```

For a remote server, establish an SSH tunnel from your own computer, then open the address above:

```sh
ssh -N -L 127.0.0.1:8788:127.0.0.1:8788 USER@SERVER
```

For a public domain, HTTPS, Cloudflare, or BaoTa deployment, see the [public deployment guide](docs/PUBLIC.md).

### 3. Connect Your Mailbox

In the dashboard, **add a primary account → configure mail retrieval → connect to Apple → sync addresses**, then copy a retrieval link or add selected addresses to a mailbox pool. Enable automatic creation and automatic session maintenance as needed.

Direct iCloud IMAP retrieval uses an **Apple app-specific password**, configured separately from the account password used to manage Hide My Email addresses and log in to Apple again.

### Updates and Source Builds

Before updating, back up the database, keys, and mail archives. Preserve `.env` and existing data volumes, then run:

```sh
git pull --ff-only
docker compose pull
docker compose up -d --wait
```

To build from source:

```sh
docker compose -f compose.yaml -f compose.build.yaml up -d --build --wait
```

See [installation and upgrades](DELIVERY.md) for backups, migration, offline installation, and deployment of pinned versions.

## API Integration

Use the API Key for the relevant mailbox from the dashboard in your program to retrieve verification codes:

```sh
curl -H 'Authorization: Bearer MAILBOX_API_KEY' \
  https://HOST/api/v1/otp
```

The example configuration returns the latest verification code by default. You can also use the retrieval link generated by the dashboard directly. `root+tag@domain` is routed to the root mailbox only when the corresponding root address is registered. Exact matches for the full address take precedence, and the domain remains unchanged.

For project integration, see [mailbox pools and API](docs/POOL.md); full request definitions are in [OpenAPI](docs/openapi.yaml). The deployed `/docs/` endpoint also provides integration instructions for the running service version.

## Automatic Creation Limits

The following are **local limits on automatic attempts per primary account over a rolling hour**, not official Apple quotas or guarantees of successful creations.

| Connection status | Automatic plan |
| --- | --- |
| Both new and legacy channels are healthy | **19 attempts** on the new channel + **4 attempts** on the legacy channel, totaling **23 attempts / hour**. |
| New channel has expired; legacy channel and directory are healthy | Falls back to **5 attempts / hour** on the legacy channel. The dual-channel rules resume after re-login on the new channel. |
| Legacy channel directory validation has failed | Keeps the plan but pauses creation until the required connections recover. |

**Why not use the full 20 + 5?** Observations show that the new and legacy channels often hit limits around 20 and 5 attempts, respectively. The dual-channel plan therefore leaves one attempt of headroom on each channel and spreads attempts over time, reducing waits caused by overlapping rolling windows or actions from other clients. When Apple explicitly rate-limits a channel, its cooldown and any longer `Retry-After` are respected.

Failed attempts may also consume the attempt allowance. Manual creation and probes are not subject to these local automatic limits, but remain subject to Apple's actual limits. See [automatic creation and channel recovery](docs/AUTO_CREATION_CHANNELS.md) for more rules.

## Before You Start

- **The new channel may still expire after approximately 8 hours.** The current renewal mechanism has not been shown to resolve this behavior. Optional automatic session maintenance logs in again when needed, but still depends on the password, code retrieval service, and Apple account status. See [automatic session maintenance](docs/AUTO_LOGIN.md).
- **Code retrieval speed depends on delivery and upstream connections.** On-demand incremental reads, connection reuse, and batch processing reduce duplicate work. Actual latency also depends on whether mail has arrived and the network between your server and the upstream service.
- **Login and mail retrieval use separate connections.** A healthy Apple management connection does not mean the IMAP credentials are valid. The two can be maintained separately.
- **Keep credentials and backups secure.** Login passwords and code retrieval URLs are stored encrypted and are not displayed back to users; the running service can decrypt them for use. Migration requires both the database and the original keys.
- **Connects directly to Apple services by default.** Mail retrieval uses standard IMAP access supported by Apple. Hide My Email management adapts the interfaces used by Apple's websites; their behavior may change with website updates, account regions, and account status. This is an independently maintained, self-hosted tool with no affiliation with or endorsement by Apple.

## Documentation

Detailed documentation is currently in Chinese.

| Topic | Resources |
| --- | --- |
| Installation, updates, backups, and migration | [Installation and upgrades](DELIVERY.md) · [Public access and HTTPS](docs/PUBLIC.md) |
| Mailbox pools and programmatic integration | [Pool API](docs/POOL.md) · [OpenAPI](docs/openapi.yaml) |
| Creation plans and login recovery | [Automatic creation](docs/AUTO_CREATION_CHANNELS.md) · [Automatic session maintenance](docs/AUTO_LOGIN.md) |
| Mail retrieval and configuration details | [Mail reception](docs/MAIL-RECEPTION.md) · [Configuration reference](docs/REFERENCE.md) |

## Acknowledgments

- **[LINUX DO](https://linux.do)** — Community support and inspiration

## Origin and Licensing

See [LICENSE](LICENSE) for the MIT license text, and [NOTICE.md](NOTICE.md#english) for its scope, upstream attribution, and third-party licensing.

<p align="right">
  <sub>Thanks to the original author, <a href="https://github.com/mangobubu">mangobubu</a>. Developed from <a href="https://github.com/mangobubu/icloud-api">icloud-api</a>, with upstream attribution and Git history preserved.</sub>
</p>
