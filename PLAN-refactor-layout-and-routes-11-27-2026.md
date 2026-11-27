# Refactor Layout, Service Extraction, and Route/Job Splitting Plan

Reorganize the Go codebase to separate `nodemanager` and `jobworker` into top-level directories, move cross-service packages into `shared/`, split `jobworker` jobs into one file per job, and split `internal/dashboard` routes into category packages with one route per file.

## Target Directory Layout

```
cmd/
  apiserver/
  dev_db_seed/
  jobworker/
  nodemanager/          # renamed from cmd/worker
nodemanager/            # extracted daemon code (docker, volume, loop, handler, middleware, presigned_backup)
jobworker/
  jobs/                 # extracted queue runner + one file per job/cron
shared/
  authkit/              # existing module
  config/               # extracted from internal/config
  domain/               # extracted from internal/domain
  storage/              # extracted from internal/storage
  nodemanagerclient/    # extracted client.go + types.go from internal/worker
  backup/               # extracted from internal/backup
  hosting/              # extracted from internal/hosting
internal/
  admin/                # admin dashboard routes (one route per file)
  billing/              # existing billing service + Stripe API handler
  billings/             # billing dashboard routes (one route per file)
  dashboard/            # shared HTML renderer, templates/*, route wiring
  jobs/                 # apiserver-only HTTP handler (handler.go)
  mcservers/            # minecraft server dashboard routes (one route per file)
  middleware/           # apiserver-only Gin middleware
  users/                # auth & user management dashboard routes (one route per file)
```

---

### Phase 1: Rename `cmd/worker` to `cmd/nodemanager` [STATUS: DONE]
- Goal: Rename the worker daemon binary entrypoint and its build/CI references to `nodemanager` without changing runtime behavior or env var contracts.
- Tasks:
  - [x] Move `cmd/worker/main.go` and `cmd/worker/main_test.go` to `cmd/nodemanager/` and remove `cmd/worker/`.
  - [x] Rename `Dockerfile.worker` to `Dockerfile.nodemanager`, building `./cmd/nodemanager` to `/app/nodemanager`.
  - [x] Update `dev-docker-build-image.sh` to build with `-f Dockerfile.nodemanager`.
  - [x] Update `.github/workflows/ci.yml.temp` build step to `go build -v -o /dev/null ./cmd/nodemanager`.
  - [x] Update `cmd/worker` references in `docs/production-deployment.md`, `docs/worker-deployment.md`, `scripts/backup-ext4-img-to-s3.sh`, and `scripts/restore-ext4-img-from-s3.sh`.
- Done when: `go build ./cmd/nodemanager ./cmd/apiserver ./cmd/jobworker ./cmd/dev_db_seed` and `go test -race ./cmd/nodemanager/...` pass, and `grep -rn "cmd/worker" cmd/ Dockerfile* dev-docker-build-image.sh .github/` returns no matches.

### Phase 2: Extract shared foundation packages to `shared/` [STATUS: DONE]
- Goal: Move low-level packages shared across `apiserver`, `jobworker`, `nodemanager`, and `dev_db_seed` out of `internal/` into `shared/`.
- Tasks:
  - [x] Move `internal/config` to `shared/config` and update all imports.
  - [x] Move `internal/domain` to `shared/domain` and update all imports.
  - [x] Move `internal/storage` to `shared/storage` and update all imports.
  - [x] Extract `internal/worker/client.go` and `internal/worker/types.go` into `shared/nodemanagerclient` (package `nodemanagerclient`) and update callers in `internal/worker`, `internal/hosting`, `internal/backup`, `internal/jobs`, `internal/dashboard`, `cmd/apiserver`, `cmd/jobworker`, and `cmd/nodemanager`.
  - [x] Remove the emptied `internal/config`, `internal/domain`, and `internal/storage` directories.
- Done when: `go build ./...` and `go test -race ./...` pass, and `shared/nodemanagerclient` has zero Docker SDK or `golang.org/x/sys/unix` imports.

### Phase 3: Extract shared domain services (`backup`, `hosting`) to `shared/` [STATUS: DONE]
- Goal: Move `backup` and `hosting` packages shared between `apiserver` and `jobworker` into `shared/`.
- Tasks:
  - [x] Move `internal/backup` to `shared/backup` and update all imports across `cmd/` and `internal/`.
  - [x] Move `internal/hosting` to `shared/hosting` and update all imports across `cmd/` and `internal/`.
  - [x] Remove the emptied `internal/backup` and `internal/hosting` directories.
- Done when: `go build ./...` and `go test -race ./shared/... ./internal/... ./cmd/...` pass.

### Phase 4: Extract `cmd/nodemanager` implementation to `./nodemanager` [STATUS: DONE]
- Goal: Move all node-daemon implementation files out of `internal/worker` into top-level `./nodemanager`.
- Tasks:
  - [x] Move `docker.go`, `handler.go`, `loop_linux.go`, `loop_linux_test.go`, `loop_other.go`, `middleware.go`, `presigned_backup.go`, `volume.go`, and `worker_test.go` from `internal/worker/` to `nodemanager/` (package `nodemanager`).
  - [x] Update `cmd/nodemanager/main.go` and `cmd/nodemanager/main_test.go` to import `nodemanager` (and `shared/nodemanagerclient` where needed).
  - [x] Remove the emptied `internal/worker/` directory.
- Done when: `go build ./...` and `go test -race ./nodemanager/... ./cmd/nodemanager/...` pass, `internal/worker` no longer exists, and `go list -f '{{.Deps}}' ./cmd/nodemanager` contains no `internal/` or AWS/S3 SDK packages.

### Phase 5: Extract `cmd/jobworker` queue to `./jobworker/jobs` and split one job per file [STATUS: DONE]
- Goal: Move the async job queue out of `internal/jobs` into `jobworker/jobs/` and break `queue.go` into one file per job/cron.
- Tasks:
  - [x] Move `internal/jobs/queue.go` and `internal/jobs/queue_test.go` to `jobworker/jobs/`.
  - [x] Keep `internal/jobs/handler.go` in `internal/jobs` (updating it to wrap `*jobs.Service` from `jobworker/jobs`) so `jobworker/jobs` has no Gin HTTP dependency.
  - [x] Split `jobworker/jobs/queue.go` into:
    - `service.go` (`Service`, `NewService`, `SetTimings`, `Start`, `drainReadyJobs`, `ProcessNext`, `executeJob`, `handleJobFailure`, `createJob`, `notifyWorker`, `GetJob`, `ListServerJobs`, `RetryJob`)
    - `create_server.go` (`EnqueueCreateServer`, `EnqueueCreateServerWithExpiry`, execution helper)
    - `delete_server.go` (`EnqueueDeleteServer`, execution helper)
    - `backup_server.go` (`EnqueueBackup`, execution helper)
    - `restore_server.go` (`EnqueueRestore`, execution helper)
    - `backup_image.go` (`EnqueueImageBackup`, execution helper)
    - `restore_image.go` (`EnqueueImageRestore`, execution helper)
    - `process_expired_servers.go` (`ProcessExpiredServers`)
    - `cleanup_retention_servers.go` (`CleanupRetentionServers`)
    - `lifecycle_cron.go` (`StartLifecycleCron`)
    - `enqueue_all_image_backups.go` (`EnqueueAllImageBackups`, `EnqueueScheduledImageBackups`)
    - `image_backup_cron.go` (`StartImageBackupCron`)
  - [x] Update imports in `cmd/jobworker/main.go`, `cmd/apiserver/main.go`, `internal/jobs/handler.go`, and `internal/dashboard/`.
- Done when: `go build ./...` and `go test -race ./jobworker/... ./internal/jobs/... ./cmd/...` pass, and `go list -f '{{.Deps}}' ./cmd/jobworker` contains no `internal/` packages.

### Phase 6: Extract shared dashboard renderer and helpers in `internal/dashboard/view` [STATUS: DONE]
- Goal: Export template rendering, auth/admin guard, and pagination helpers from `internal/dashboard/view` so category route packages can depend on them without circular imports.
- Tasks:
  - [x] Move `internal/dashboard/templates` to `internal/dashboard/view/templates` and create `internal/dashboard/view/renderer.go` exporting `Renderer` (`RenderView`, `RenderViewWithStatus`, `RenderAdminView`).
  - [x] Create `internal/dashboard/view/helpers.go` exporting shared helpers used across route categories (`RequireAdmin`, `EffectiveServerUserID`, `ParseIDParam`, `ServerRedirectURL`, `ServerExpiryFlags`, `Pagination`, `ParseSearchAndPage`, `ApplySearchFilter`, `PaginateQuery`, `AdminPageURL`, `LoadUserEmailMap`, `LoadServerNameMap`).
  - [x] Refactor `internal/dashboard/dashboard.go` and `internal/dashboard/admin.go` to delegate to `view.Renderer` and the exported helpers.
- Done when: `go test -race ./internal/dashboard/...` passes with zero behavior or route changes.

### Phase 7: Split `users` and `billings` routes into `internal/users/` and `internal/billings/` [STATUS: DONE]
- Goal: Move auth/user management routes and billing/purchase routes out of `internal/dashboard/dashboard.go` into dedicated category packages with one route per file.
- Tasks:
  - [x] Create `internal/users/` with `handler.go` (struct + `RegisterRoutes`) and one file per route:
    - `login.go` (`GET/POST /login`)
    - `register.go` (`GET/POST /register`)
    - `logout.go` (`GET /logout`)
    - `list_users.go` (`GET /dashboard/users`)
    - `update_user_role.go` (`POST /dashboard/users/:id/role`)
  - [x] Create `internal/billings/` (plural to avoid colliding with `internal/billing`) with `handler.go` (struct + `RegisterRoutes`) and one file per route:
    - `buy_server.go` (`GET/POST /dashboard/servers/buy`)
    - `extend_server.go` (`POST /dashboard/servers/:id/extend`)
    - `show_billing.go` (`GET /dashboard/billing`)
  - [x] Wire `users.Handler` and `billings.Handler` in `internal/dashboard/dashboard.go` `RegisterRoutes` and remove the migrated methods from `dashboard.go`.
- Done when: `go build ./...` and `go test -race ./internal/dashboard/... ./internal/users/... ./internal/billings/...` pass.

### Phase 8: Split Minecraft server routes into `internal/mcservers/` [STATUS: DONE]
- Goal: Move all server dashboard routes out of `internal/dashboard/dashboard.go` into `internal/mcservers/` with one route per file.
- Tasks:
  - [x] Create `internal/mcservers/` with `handler.go` (struct + `RegisterRoutes`) and one file per route:
    - `list_servers.go` (`GET /dashboard`)
    - `create_server.go` (`GET/POST /dashboard/servers/new`)
    - `server_detail.go` (`GET /dashboard/servers/:id`)
    - `server_console.go` (`GET /dashboard/servers/:id/console`)
    - `server_console_logs.go` (`GET /dashboard/servers/:id/console/logs`)
    - `send_console_command.go` (`POST /dashboard/servers/:id/console/command`)
    - `server_backups.go` (`GET /dashboard/servers/:id/backups`)
    - `trigger_backup.go` (`POST /dashboard/servers/:id/backups/now`)
    - `restore_backup.go` (`POST /dashboard/servers/:id/backups/:backup_id/restore`)
    - `server_logs.go` (`GET /dashboard/servers/:id/logs`)
    - `server_settings.go` (`GET/POST /dashboard/servers/:id/settings`)
    - `server_jobs.go` (`GET /dashboard/servers/:id/jobs`)
    - `retry_job.go` (`POST /dashboard/servers/:id/jobs/:job_id/retry`)
    - `server_host.go` (`GET /dashboard/servers/:id/host`)
    - `trigger_migration.go` (`POST /dashboard/servers/:id/migrate`)
    - `start_server.go` (`POST /dashboard/servers/:id/start`)
    - `stop_server.go` (`POST /dashboard/servers/:id/stop`)
    - `delete_server.go` (`POST /dashboard/servers/:id/delete`)
    - `open_file_manager.go` (`GET /dashboard/servers/:id/files/open`)
  - [x] Wire `mcservers.Handler` in `internal/dashboard/dashboard.go` `RegisterRoutes` and remove the migrated methods from `dashboard.go`.
- Done when: `go build ./...` and `go test -race ./internal/dashboard/... ./internal/mcservers/...` pass.

### Phase 9: Split admin routes into `internal/admin/` and final verification [STATUS: DONE]
- Goal: Move all `/dashboard/admin/*` routes out of `internal/dashboard/admin.go` into `internal/admin/` (one route per file) and verify the full platform build and test suite.
- Tasks:
  - [x] Create `internal/admin/` with `handler.go` (struct + `RegisterRoutes` + `parsePlanForm`) and one file per route:
    - `list_servers.go` (`GET /dashboard/admin/servers`)
    - `list_orders.go` (`GET /dashboard/admin/orders`)
    - `list_payments.go` (`GET /dashboard/admin/payments`)
    - `list_jobs.go` (`GET /dashboard/admin/jobs`)
    - `list_logs.go` (`GET /dashboard/admin/logs`)
    - `list_plans.go` (`GET /dashboard/admin/plans`)
    - `create_plan.go` (`POST /dashboard/admin/plans`)
    - `update_plan.go` (`POST /dashboard/admin/plans/:id`)
    - `delete_plan.go` (`POST /dashboard/admin/plans/:id/delete`)
  - [x] Wire `admin.Handler` in `internal/dashboard/dashboard.go` `RegisterRoutes` and remove `internal/dashboard/admin.go`.
  - [x] Run full build, test suite, and compose config validation.
- Done when: `go build ./...`, `go test -race ./...`, and `docker compose -f docker-compose.base.yml -f docker-compose.app.yml config --quiet` all pass, and `internal/dashboard/dashboard.go` contains only service wiring and root redirect.

---

## Unknowns, risks, assumptions

- **Docker / env var compatibility**: Phase 1 renames `cmd/worker` and `Dockerfile.worker` to `cmd/nodemanager` and `Dockerfile.nodemanager`, while keeping existing env vars (`WORKER_PORT`, `WORKER_API_KEY`, `WORKER_IMAGE`, `DEFAULT_WORKER_URL`, etc.) and Compose service/container names (`worker` / `mc_worker`) unchanged so `.env`, `dev-vps.env`, and `tests/test-disk-limit.sh` do not break. If you also want env vars, image names, and Compose service names renamed to `nodemanager`, that should be a separate follow-up phase.
- **Single Go module**: `nodemanager/`, `jobworker/`, and `shared/{config,domain,storage,nodemanagerclient,backup,hosting}` remain packages in the root Go module (`github.com/dreamoutbox/minecraft-server-hosting-platform`), rather than separate `go.mod` submodules. This avoids `go.work` churn while enforcing clean directory boundaries.
- **File naming**: Plan uses standard Go `snake_case.go` filenames (e.g. `create_server.go` instead of `create-server.go`) consistently across `internal/{users,billings,mcservers,admin}` and `jobworker/jobs/`.
- **GET+POST form pairs**: Paired form routes on the same URL (e.g. `GET/POST /login`, `GET/POST /register`, `GET/POST /dashboard/servers/new`, `GET/POST /dashboard/servers/buy`, `GET/POST /dashboard/servers/:id/settings`) are kept together in one file per route path.
- **Shared `internal/billing` vs `internal/billings`**: The existing `internal/billing` package (Stripe provider, billing domain service, JSON API) stays as-is; dashboard billing routes use `internal/billings` to avoid name collisions.
- **Test DB dependency**: Full `go test -race ./...` requires PostgreSQL (`TEST_DATABASE_DSN`) for DB-backed packages; packages without DB dependencies (`nodemanager`, `shared/nodemanagerclient`, `shared/config`, `shared/storage`, `internal/middleware`) run standalone.
