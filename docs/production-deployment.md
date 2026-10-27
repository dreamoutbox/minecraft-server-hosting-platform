# Production Deployment Guide

This guide details operational prerequisites, deployment steps, and security boundaries for deploying the Minecraft Server Hosting Platform in production.

## System Architecture Overview

The platform consists of two primary binaries:
1. **Web API Orchestrator** (`cmd/server`): Central manager for user authentication, billing, scheduling, port allocations, and user dashboard. Never touches remote Docker daemons directly.
2. **Worker Daemon** (`cmd/worker`): Node-level agent running on each hosting machine. Manages Docker containers and ext4 loop mounts exclusively on that node. Authenticated via per-host static secret API keys.

---

## Worker Daemon Node Deployment

### Hard Operational Requirements

- **Linux Capabilities**: Worker process or its container **must** run with `--cap-add=SYS_ADMIN`. This is mandatory for loop mounting sparse ext4 filesystem images.
- **Device Access**: Read/write access to `/dev/loop-control` and `/dev/loop*` block devices.
- **Docker Engine**: Local access to `/var/run/docker.sock`.
- **Port Ranges**: Node firewall must allow inbound TCP traffic across the configured port allocation range (e.g. 25565-25600).

### Worker Environment Variables

| Variable | Default | Description |
|---|---|---|
| `WORKER_PORT` | `9090` | Port on which the worker HTTP service listens |
| `WORKER_API_KEY` | *(required)* | Secret authentication key issued during host registration |
| `WORKER_BASE_DIR` | `/var/lib/mc-platform` | Filesystem root for images and mount directories |
| `WORKER_MAX_SERVERS` | `20` | Server hosting capacity ceiling for this node |
| `WORKER_LOOP_DEVICE_ENABLED` | `true` | Enables real ext4 loop device mounting (requires SYS_ADMIN) |

---

## Web API Orchestrator Deployment

### Environment Variables

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | Port for the HTTP web server and API |
| `DB_HOST` | `localhost` | PostgreSQL host |
| `DB_PORT` | `5432` | PostgreSQL port |
| `DB_USER` | `postgres` | PostgreSQL username |
| `DB_PASSWORD` | `postgrespassword` | PostgreSQL password |
| `DB_NAME` | `minecraft_hosting` | PostgreSQL database name |
| `DB_SSLMODE` | `disable` | PostgreSQL SSL mode (`require` or `verify-full` in prod) |
| `PORT_RANGE_MIN` | `25565` | Lower bound of cluster-wide game server ports |
| `PORT_RANGE_MAX` | `25600` | Upper bound of cluster-wide game server ports |
| `JWT_SECRET` | *(required)* | Cryptographic HMAC secret for signing user tokens |
| `BILLING_PROVIDER` | `mock` | Billing gateway: `stripe` or `mock` |
| `STRIPE_SECRET_KEY` | *(required if stripe)* | Stripe API secret key (`sk_live_...`) |
| `STRIPE_WEBHOOK_SECRET`| *(required if stripe)* | Stripe webhook signature signing secret (`whsec_...`) |
| `STORAGE_TYPE` | `s3` | Object storage variant: `s3` or `memory` |
| `S3_ENDPOINT` | *(empty for AWS)* | S3 endpoint URL (set for SeaweedFS or custom S3) |
| `S3_REGION` | `us-east-1` | AWS S3 region |
| `S3_BUCKET` | `minecraft-backups`| S3 bucket name |
| `S3_ACCESS_KEY` | *(optional)* | AWS access key ID |
| `S3_SECRET_KEY` | *(optional)* | AWS secret access key |
| `S3_USE_PATH_STYLE` | `false` | Set `true` for SeaweedFS, `false` for AWS S3 |

---

## Cluster Host Enrollment & Key Rotation

1. **Enroll Host**:
   Admin invokes `POST /api/v1/hosts` with `hostname`, `worker_base_url`, and `max_servers`. The orchestrator returns the generated API key once.
2. **Configure Worker**:
   Admin sets `WORKER_API_KEY` on the node's worker daemon service.
3. **Key Rotation**:
   Admin invokes `POST /api/v1/hosts/:id/rotate-key`. The response yields a new key. Update `WORKER_API_KEY` on the worker node.
