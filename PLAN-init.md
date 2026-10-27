# PLAN: Minecraft Server Hosting Platform

Stack: Go (Gin + GORM), PostgreSQL, Bootstrap frontend, Docker (per-server containers, port-mapped), Stripe (payment gateway only — plans/subscriptions managed in our DB), SeaweedFS (dev S3), cron backups, manual backup/restore triggers, monthly billing, dashboard module separated from hosting-core module.

## Architecture

- **Web API** (`cmd/server`): central orchestrator. Owns scheduling, port allocation, billing, dashboard. Never touches Docker directly on remote hosts.
- **Worker daemon** (`cmd/worker`): runs on each hosting node. Owns local Docker lifecycle + loop-mounted volume provisioning for that node. Exposes an HTTP API, authenticated via a per-host API key (issued at host registration, stored hashed in `Host`, sent as a bearer/header key by the Web API).
- Web API → Worker daemon is the only path to Docker; this also resolves remote-Docker-endpoint exposure risk from earlier (no raw Docker TCP exposed across hosts).

Decisions locked in:
- Auth (user-facing): JWT.
- Auth (API → worker): per-host static API key, rotatable, checked on every worker request.
- Migrations: GORM AutoMigrate.
- Scheduler: least-loaded host (lowest active-server-count / capacity ratio); reject if none qualify.
- Networking: Docker bridge mode with per-container published ports.
- Port allocation: **globally unique across the cluster** (single pool, not per-host) — required because migration must be able to preserve a server's port when moving hosts, and external routing (see Unknowns) is assumed to route by port alone.
- Disk limits: loop-mounted sparse files per server, provisioned by the worker daemon on its local host. Requires `--cap-add=SYS_ADMIN` (not full `--privileged`) plus access to `/dev/loop-control` and loop devices on the worker daemon's deployment — call this out explicitly wherever the worker is deployed (bare host service or container).
- Stripe: payment gateway only (charge a card / saved payment method). No Stripe Products/Prices/Subscriptions objects. Plan catalog and subscription/billing-cycle state live entirely in our own DB; our cron drives when a charge happens.
- Plans: Small (1 CPU / 1GB RAM / 10GB disk), Medium (2 CPU / 2GB RAM / 20GB disk), Large (4 CPU / 4GB RAM / 40GB disk).
- Migration: moving a running server to a different host is a first-class operation, built on the backup/restore primitives, preserving the server's port.

## Phase 1: Project skeleton & core domain [DONE]
- Goal: Repo builds, connects to Postgres, module boundaries in place for both binaries.
- Tasks:
  - [x] Init Go module; layout: `/internal/hosting`, `/internal/billing`, `/internal/dashboard`, `/internal/storage`, `/internal/backup`, `/internal/worker` (shared client/types), `/cmd/server`, `/cmd/worker`
  - [x] Gin bootstrap for `cmd/server` with health check route
  - [x] GORM + Postgres connection, config via env
  - [x] Core models: `User`, `Plan` (name, cpu_limit, mem_limit, disk_limit, stripe-unrelated — no price_id here), `Host` (hostname/address, worker_base_url, api_key_hash, max_servers, status), `Server` (status, port, container_id, plan_id, host_id, mount_path), `Subscription` (plan_id, status, next_billing_date — no stripe subscription id), `PaymentRecord`, `BackupRecord`
  - [x] Migrations via GORM AutoMigrate, run on app startup
  - [x] Seed `Plan` rows: Small (1/1/10), Medium (2/2/20), Large (4/4/40)
  - [x] Config: global `PORT_RANGE_MIN`/`PORT_RANGE_MAX` env vars, validated at startup (min < max)
  - [x] docker-compose.dev.yml: postgres, seaweedfs
- Done when: `go build ./...` succeeds for both binaries, app boots, migrates schema against docker-compose postgres, `/health` returns 200.

## Phase 2: Auth & user management [DONE]
- Goal: Users can register/login; sessions protect routes.
- Tasks:
  - [x] Password hashing (bcrypt/argon2), register/login/logout endpoints
  - [x] JWT issuance (access + refresh token) and auth middleware
  - [x] User CRUD (self-service profile)
- Done when: Auth integration tests pass; protected route returns 401 without valid/expired JWT.

## Phase 3: Worker daemon (`cmd/worker`) [DONE]
- Goal: A standalone daemon that manages Docker containers and loop-mounted volumes on one host, reachable only via authenticated HTTP API.
- Tasks:
  - [x] HTTP server with API-key middleware: reject any request without a valid key matching this host's configured secret (constant-time compare)
  - [x] Docker SDK integration (not raw `exec`) scoped to the local Docker socket only
  - [x] Endpoints: `POST /containers` (create+start, given plan limits + port + mount path), `POST /containers/:id/stop`, `DELETE /containers/:id`, `GET /containers/:id/status`
  - [x] Volume provisioning endpoints: `POST /volumes` (fallocate sparse file sized to plan's disk_limit, `mkfs.ext4`, `mount -o loop`), `DELETE /volumes/:id` (unmount + remove image file)
  - [x] Volume export/import endpoints for backup/restore and migration: `GET /volumes/:id/export` (streams tar of mounted contents), `POST /volumes/:id/import` (stops write access, replaces contents from uploaded tar)
  - [x] Deployment doc/task: worker process or its container must run with `--cap-add=SYS_ADMIN` and access to `/dev/loop-control` + loop devices — document this as a hard requirement, not optional
  - [x] Local health/capacity endpoint: `GET /status` (active container count, free disk) for the scheduler to poll or for the host to self-report on registration/heartbeat
- Done when: Worker daemon running standalone can create a container with a loop-mounted, size-capped volume on its host, reachable only with a valid API key; export/import round-trips volume contents correctly.

## Phase 4: Hosting core (`internal/hosting`, Web API side) [DONE]
- Goal: Central orchestrator schedules servers across hosts via the worker daemon API, with cluster-wide unique ports, isolated from billing/dashboard code.
- Tasks:
  - [x] Worker client: typed HTTP client wrapping each `Host`'s worker API, injecting that host's API key
  - [x] Cluster scheduler: pick a `Host` with available capacity (least-loaded by active-server-count vs `max_servers`); reject if none qualify
  - [x] Global port allocator: DB unique constraint on `port` alone (not scoped to host), reserve from `PORT_RANGE_MIN`/`PORT_RANGE_MAX`, transactional allocate-then-create with retry on conflict
  - [x] Server lifecycle endpoints: create, start, stop, delete, status; create requires `plan_id`; orchestrator calls scheduler → worker client for volume + container provisioning
  - [x] Host registration endpoint/flow: generates and returns the API key once (stored hashed), admin configures it into that host's worker daemon
- Done when: Can create a server via API with a plan, scheduler places it on a host with capacity, worker daemon provisions the loop-mounted volume + container, server reachable on its globally-unique assigned port, stop/delete tears down container + volume + frees the port via the worker API.

## Phase 5: Storage abstraction (SeaweedFS dev / S3 prod) [DONE]
- Goal: Pluggable object storage interface used by backup/restore and migration.
- Tasks:
  - [x] `StorageProvider` interface: `Put`, `Get`, `Delete`, `List`
  - [x] SeaweedFS S3-compatible implementation (dev)
  - [x] Real S3 implementation (prod) — same interface, swap via config
- Done when: Integration test writes/reads/deletes an object against SeaweedFS in docker-compose.

## Phase 6: Backup & restore (`internal/backup`) [DONE]
- Goal: World data backed up on schedule and on-demand; restorable. Built on the worker's export/import endpoints.
- Tasks:
  - [x] Dedicated backup function: call worker's volume export endpoint for the server's host → push resulting stream via `StorageProvider`
  - [x] Dedicated restore function: pull from storage → call worker's volume import endpoint (worker stops container first if running) → restart
  - [x] Cron scheduler (`robfig/cron`) sweeping active servers on schedule
  - [x] Manual trigger endpoints (backup now, restore from a given snapshot) — reuse the same functions cron uses
  - [x] `BackupRecord` tracking: timestamp, size, storage key, status
  - [x] Keep backup/restore functions callable from a future admin CLI (no web-request-specific state; explicit context/params)
- Done when: Manual backup produces an object in SeaweedFS via the worker export path; restore on the same or a different host reproduces world data; cron job runs on schedule in dev.

## Phase 7: Server migration [DONE]
- Goal: Move a running server to a different host with minimal downtime, preserving its port.
- Tasks:
  - [x] `Server.status = migrating`; block other lifecycle actions on it while in this state
  - [x] Trigger a fresh backup via Phase 6's backup function to guarantee an up-to-date snapshot
  - [x] Scheduler selects a destination host with capacity whose worker can accept the server's **existing port** (reject migration if the port can't be preserved — don't silently reassign, since that breaks the port-routing assumption)
  - [x] Provision volume + container on destination host via worker API, restore from the backup snapshot
  - [x] Health-check the new container (e.g. port open / basic server-list ping) before tearing down the source — blue/green, don't destroy source until destination is verified
  - [x] On success: update `Server.host_id`/`container_id`/`mount_path`, tear down source container + volume via source worker API, release source-side worker resources
  - [x] On failure: leave source running untouched, mark migration failed, clean up any partial destination-side resources
- Done when: A running server can be migrated to a different host via API/admin trigger, keeps its port, world data is intact post-migration, source host resources are fully released, and a forced failure mid-migration leaves the original server running and reachable.

## Phase 8: Billing (`internal/billing`) [DONE]
- Goal: Monthly billing driven by our own subscription state, using Stripe purely to move money.
- Tasks:
  - [x] `PaymentProvider` interface: `CreatePayment`, `GetPayment`, `Refund`, `SavePaymentMethod`, `ChargeSavedPaymentMethod` (no `CreateSubscription`/`CancelSubscription` — subscription *state* is ours)
  - [x] Mock implementation: in-memory/DB-backed, deterministic responses, for dev config
  - [x] Real Stripe implementation using Stripe Go SDK (PaymentIntents / saved PaymentMethods, not Stripe Subscriptions)
  - [x] Config switch (env var) selects provider — wired via DI at startup
  - [x] Our own billing cron: for each active `Subscription` whose `next_billing_date` has passed, call `ChargeSavedPaymentMethod` for the plan's price (price stored in our `Plan`/config, not Stripe), record result, advance `next_billing_date` on success
  - [x] Stripe webhook endpoint (payment succeeded/failed) with mandatory signature verification (`Stripe-Signature` header + webhook secret) — reject unverified requests outright
  - [x] Mock provider also emits equivalent local "webhook" events so dev exercises the same handler path as prod
  - [x] Link subscription status to server lifecycle: on repeated charge failure, suspend the server (stop container via hosting module, keep volume intact) rather than deleting
- Done when: Mock provider drives a full charge→renew→charge-fails→suspend cycle in tests; webhook handler verifies signatures and rejects tampered/unsigned payloads; billing cron correctly advances/stalls `next_billing_date` based on charge outcome.

## Phase 9: Dashboard module (`internal/dashboard` + Bootstrap frontend) [DONE]
- Goal: Web UI for server management, file management, billing status, migration — decoupled from hosting-core.
- Tasks:
  - [x] Server templates (Go `html/template`) with Bootstrap layout
  - [x] Dashboard calls into `hosting`, `backup`, `billing` packages only through their public interfaces
  - [x] Server list/detail/create/delete UI; create flow includes plan selection (CPU/RAM/disk + price per plan)
  - [x] File management UI: browse/upload/download world files (via storage interface, scoped to that server's backup prefix)
  - [x] Manual backup/restore buttons wired to Phase 6 endpoints
  - [x] Admin: manual migration trigger UI (pick destination host or let scheduler choose)
  - [x] Billing/subscription status + saved payment method UI
- Done when: End-to-end flow works through the browser: register → add payment method (mock) → create server → see it running → manage files → trigger backup → trigger migration.

## Phase 10: Hardening & polish
- Goal: Production-readiness pass.
- Tasks:
  - [ ] Rate limiting on auth and payment endpoints
  - [ ] Structured logging + request IDs, propagated into worker daemon calls for cross-service tracing
  - [ ] Input validation on all handlers (file paths — path traversal risk in file manager; server name constraints)
  - [ ] Worker API key rotation flow (issue new key, update worker config, revoke old)
  - [ ] Dockerfile for both binaries + production deploy notes (worker daemon deployment with required caps documented)
  - [ ] Basic test coverage report, CI pipeline (lint + test + build for both binaries)
- Done when: CI green on lint/test/build; manual security pass on file manager, webhook signature verification, and worker API key handling confirmed.

## Unknowns, risks, assumptions
- Global port uniqueness implies some external routing layer that forwards traffic to the correct host based on port alone (e.g. a shared public IP with per-port forwarding rules, or a reverse proxy). That routing layer itself is **not** covered by this plan — whoever owns it must be updated whenever a server is created, deleted, or migrated. Flag before Phase 4 if this needs to be built too.
- Worker daemon deployment target (bare VM service vs container) isn't specified — `--cap-add=SYS_ADMIN` + loop device access is required either way, but a containerized worker also needs the Docker socket mounted in, which is itself a significant privilege boundary to document for ops.
- Scheduler is still "least-loaded, reject if none qualify" — no rebalancing or automatic migration on host failure; Phase 7 gives you the primitive to migrate manually/by admin trigger, not an automated failover controller.
- Stripe price per plan now needs to live in our own config/DB instead of Stripe — confirm actual monthly prices for Small/Medium/Large before Phase 8.
- Worker API key is a single static secret per host in this plan — fine for a small/trusted cluster; if hosts are in less-trusted environments later, revisit (mTLS, short-lived tokens).

