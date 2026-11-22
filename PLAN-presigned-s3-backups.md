# Plan: Presigned S3 Backups, Retention Limit & Daily ext4 Image CRON

## Overview

Architecture decisions locked in before implementation:
- **Env rename**: `RETENTION_DAYS` -> `SERVER_EXPIRED_DATA_RETENTION_DAYS` (default `7`).
- **New env vars**:
  - `CRON_MC_SERVER_DATA_BACKUP` (default `"0 5 * * *"`, container local TZ).
  - `BACKUP_MAX_COUNT` (default `8`, enforced per server independently for user file backups and node ext4 image backups).
  - `BACKUP_PRESIGN_TTL` (default `"1h"`, Go duration string, scoped strictly to single-object PUT or GET).
- **Zero S3 credentials on `cmd/worker`**: `jobworker` generates short-lived presigned S3 PUT/GET URLs and invokes `cmd/worker` HTTP endpoints; `cmd/worker` only performs plain HTTP PUT/GET against those URLs.
- **Two separate backup flows (both using presigned URLs)**:
  1. **User server backups (`backup_records`)**: on-demand `tar.gz` of mounted files (`/var/lib/mc-platform/mounts/<id>`) so users can extract standard Minecraft server files.
  2. **Node ext4 `.img` backups (`server_image_backups`)**: daily CRON (via `server_jobs` queue) over all provisioned servers (`running` and `stopped`). Quiesces running servers (`save-off` + `save-all` + OS `sync`, then `save-on`), streams `<id>.img` through `tar` + `gzip`, splits compressed output into parts of at most 1 GiB, and uploads each part via pre-generated presigned PUT URLs.
- **Admin `.img` restore**: admin-only endpoints on `cmd/apiserver` enqueue a `restore_image` job; `cmd/jobworker` generates presigned GET URLs and invokes `cmd/worker` to download, concatenate, gunzip+untar, atomically replace `<id>.img`, and remount/restart. Driven by a standalone shell script (`scripts/restore-image-backup.sh`).

---

### Phase 1: Rename retention env var and add backup config settings [STATUS: DONE]
- Goal: Rename `RETENTION_DAYS` to `SERVER_EXPIRED_DATA_RETENTION_DAYS` and expose backup CRON, max count, and presign TTL settings in config and deployment files.
- Tasks:
  - [x] Rename `RETENTION_DAYS` to `SERVER_EXPIRED_DATA_RETENTION_DAYS` (default `7`) in `internal/config/config.go`, `template.env`, and `docker-compose.app.yml` (`apiserver` and `jobworker` services).
  - [x] Add `CronMCServerDataBackup` (`CRON_MC_SERVER_DATA_BACKUP`, default `"0 5 * * *"`), `BackupMaxCount` (`BACKUP_MAX_COUNT`, default `8`), and `BackupPresignTTL` (`BACKUP_PRESIGN_TTL`, default `1h`) to `config.Config` and `LoadFromEnv()` in `internal/config/config.go`.
  - [x] Validate `CRON_MC_SERVER_DATA_BACKUP` (standard 5-field cron expression via `robfig/cron/v3`), `BACKUP_MAX_COUNT > 0`, and `BACKUP_PRESIGN_TTL > 0` in `LoadFromEnv()`.
  - [x] Document the new env vars in `template.env` and wire them into `docker-compose.app.yml` using `${VAR:-default}` syntax.
  - [x] Add unit tests in `internal/config/config_test.go` covering defaults, renamed env var, and invalid cron/duration/count values.
- Done when: `go test ./internal/config/...` and `go build ./cmd/...` pass, and no stale `RETENTION_DAYS` references remain outside `TODO.md` or historical docs.

### Phase 2: Add presigned PUT/GET URL generation to `internal/storage` [STATUS: DONE]
- Goal: Allow orchestrator/jobworker to mint object-scoped, time-limited presigned PUT and GET URLs for S3/SeaweedFS and in-memory test storage.
- Tasks:
  - [x] Extend `storage.StorageProvider` in `internal/storage/storage.go` with `PresignPut(ctx context.Context, key string, ttl time.Duration, contentType string) (string, error)` and `PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)`.
  - [x] Implement `PresignPut` and `PresignGet` on `S3StorageProvider` using `s3.NewPresignClient(s.client)` with `s3.WithPresignExpires(ttl)`, scoped strictly to the configured bucket and single key.
  - [x] Implement `PresignPut` and `PresignGet` on `MemoryStorageProvider` backed by a lightweight local HTTP handler (started lazily or via helper) so unit tests exercise real HTTP PUT/GET against presigned URLs with expiry and method enforcement.
  - [x] Add unit and SeaweedFS integration tests in `internal/storage/storage_test.go` verifying presigned PUT upload, presigned GET download, expired URL rejection, and method restriction (e.g. GET/DELETE rejected on a PUT-only URL).
- Done when: `go test ./internal/storage/...` passes (both memory tests and SeaweedFS integration test when SeaweedFS is running).

### Phase 3: Add `ServerImageBackup` model and image backup/restore job enums [STATUS: DONE]
- Goal: Persist ext4 image backup metadata (including multi-part S3 keys) and queue job/operation types in PostgreSQL.
- Tasks:
  - [x] Add `ServerImageBackup` struct (`server_image_backups` table) to `internal/domain/models.go` with `ID`, `ServerID`, `StoragePrefix`, `PartKeys` (JSON array of S3 keys), `PartCount`, `SizeBytes`, `Status` (`BackupStatus`), `CreatedAt`, `UpdatedAt`, plus helper methods to encode/decode `PartKeys`.
  - [x] Register `&ServerImageBackup{}` in `domain.AutoMigrate` (`internal/domain/db.go`).
  - [x] Add `JobTypeBackupImage` (`"backup_image"`), `JobTypeRestoreImage` (`"restore_image"`), `ServerOpImageBackup` (`"image_backup"`), and `ServerOpImageRestore` (`"image_restore"`) constants in `internal/domain/models.go`.
  - [x] Add nullable indexed `ImageBackupID *uint` field to `domain.ServerJob` so backup/restore jobs cleanly reference `server_image_backups`.
- Done when: `go test ./internal/domain/...` and `go build ./...` pass with the updated schema and enums.

### Phase 4: Add worker presigned `.tar.gz` volume backup/restore endpoints [STATUS: DONE]
- Goal: Enable `cmd/worker` to compress mounted Minecraft files to `.tar.gz` and transfer directly to/from S3 via presigned URLs without holding S3 credentials.
- Tasks:
  - [x] Add request/response DTOs (`UploadVolumeBackupRequest`, `UploadVolumeBackupResponse`, `RestoreVolumeBackupRequest`) in `internal/worker/types.go`.
  - [x] Implement gzip-aware tar export/import helpers in `internal/worker/volume.go`:
    - `ExportVolumeGzip`: tars `/var/lib/mc-platform/mounts/<id>` (skipping `lost+found`) through `gzip` into a temp file (yielding exact `Content-Length` for S3/SeaweedFS PUT), uploads via HTTP `PUT` to `PresignedURL`, and cleans up the temp file.
    - `ImportVolumeAuto`: peeks magic bytes (`0x1f 0x8b`) so both new `.tar.gz` and legacy uncompressed `.tar` archives unpack cleanly into the mount directory.
  - [x] Register `POST /volumes/:id/backup-upload` and `POST /volumes/:id/backup-restore` on `worker.Handler` (`internal/worker/handler.go`) — worker only uses standard `net/http` against the supplied presigned URL.
  - [x] Add `UploadVolumeBackup` and `RestoreVolumeBackup` to `worker.Client` / `worker.HTTPClient` (`internal/worker/client.go`), using a dedicated transfer HTTP client without the 60s fixed timeout (relying on caller `ctx` deadline) so large backups do not time out.
  - [x] Add unit tests in `internal/worker/worker_test.go` using an `httptest.Server` as a mock presigned S3 endpoint to verify `.tar.gz` upload, download+restore, and legacy `.tar` restore.
- Done when: `go test ./internal/worker/...` passes and `cmd/worker` has zero AWS/S3 SDK imports.

### Phase 5: Add worker presigned ext4 `.img` multi-part backup/restore endpoints [STATUS: DONE]
- Goal: Enable `cmd/worker` to quiesce a running server, `tar.gz` + split `/var/lib/mc-platform/images/<id>.img` into <= 1 GiB parts, upload via presigned PUT URLs, and restore/merge parts via presigned GET URLs.
- Tasks:
  - [x] Add request/response DTOs (`UploadImageBackupRequest` with `PresignedURLs`, `MaxPartBytes`, `ContainerID`; `UploadImageBackupResponse` with `PartsUploaded`, `PartSizes`, `TotalBytes`; `RestoreImageBackupRequest` with `PresignedURLs`) in `internal/worker/types.go`.
  - [x] Implement quiesce helper on `worker.Handler`: when `ContainerID != ""`, execute Minecraft `save-off` and `save-all` via `ContainerManager.ExecCommand` (deferring `save-on`) and call `unix.Sync()` before reading `<id>.img`.
  - [x] Implement `ExportImageParts` in `internal/worker/volume.go`: stream `/var/lib/mc-platform/images/<id>.img` through `tar` -> `gzip` into a bounded-size chunk writer (default `1 GiB`, configurable via `MaxPartBytes` for testing), writing temp part files, uploading each part via HTTP `PUT` with exact `Content-Length` to `PresignedURLs[i]`, validating `partCount <= len(PresignedURLs)`, and removing temp files on success or error.
  - [x] Implement `RestoreImageParts` in `internal/worker/volume.go`: unmount `/var/lib/mc-platform/mounts/<id>` and detach loop devices for `<id>.img`, download all `PresignedURLs` in order, concatenate -> `gzip.NewReader` -> `tar.NewReader` into `<id>.img.restore.tmp`, fsync, atomically `os.Rename` over `<id>.img`, and remount via `EnsureMounted` (preserving original `<id>.img` if download/extract fails).
  - [x] Register `POST /volumes/:id/image-backup` and `POST /volumes/:id/image-restore` in `internal/worker/handler.go` and add `UploadImageBackup` / `RestoreImageBackup` to `worker.Client` (`internal/worker/client.go`).
  - [x] Add unit tests in `internal/worker/worker_test.go` covering single-part upload/restore, multi-part split + merge restore (using small `MaxPartBytes` e.g. 64 KiB), quiesce command ordering (`save-off` -> `save-all` -> `save-on`), and failure rollback leaving original `.img` intact.
- Done when: `go test ./internal/worker/...` passes, multi-part `.img` round-trip matches original SHA-256 byte-for-byte, and `cmd/worker` still has zero AWS/S3 SDK imports.

### Phase 6: Switch user `backup_records` to presigned `.tar.gz` and add keep-last-N pruning [STATUS: TODO]
- Goal: Move manual server backups/restores onto the presigned `.tar.gz` worker flow and enforce the per-server backup limit (`BACKUP_MAX_COUNT`, default `8`).
- Tasks:
  - [ ] Add `SetMaxBackups(n int)` and `SetPresignTTL(d time.Duration)` (defaulting to `8` and `1h`) on `backup.Service` in `internal/backup/backup.go`.
  - [ ] Update `CreatePendingBackup` to generate `.tar.gz` keys (`backups/server-<id>/<ts>.tar.gz`).
  - [ ] Update `ExecuteBackupRecord` to mint a presigned `PUT` URL (`PresignPut`), call `wClient.UploadVolumeBackup`, persist `SizeBytes` + `BackupStatusCompleted`, and call a shared `pruneExcessBackups` helper that keeps the newest `maxBackups` completed `BackupRecord` rows per server and deletes older S3 objects + DB rows.
  - [ ] Update `RestoreServer` to mint a presigned `GET` URL (`PresignGet`), stop container, call `wClient.RestoreVolumeBackup`, and restart container.
  - [ ] Verify `internal/hosting/migration.go` remains compatible with `.tar.gz` archives returned by `BackupServer` (leveraging `ImportVolumeAuto` from Phase 4 or presigned restore).
  - [ ] Update mocks and tests in `internal/backup/backup_test.go`, `internal/hosting/migration_test.go`, and `internal/jobs/queue_test.go`, adding a test that creating 10 backups prunes down to the newest 8 in both DB and storage.
- Done when: `go test ./internal/backup/... ./internal/hosting/... ./internal/jobs/...` passes and backup pruning deletes both S3 objects and DB records beyond the configured limit.

### Phase 7: Implement ext4 image backup/restore service, job queue execution, and daily CRON in `cmd/jobworker` [STATUS: TODO]
- Goal: Orchestrate scheduled daily ext4 `.img` backups via `server_jobs` in `cmd/jobworker` with per-server 8-backup pruning and image restore execution.
- Tasks:
  - [ ] Add image backup service methods in `internal/backup/image_backup.go`:
    - `CreatePendingImageBackup(ctx, serverID)`
    - `ExecuteImageBackupRecord(ctx, userID, serverID, imageBackupID)`: pre-mints `max(server.Plan.DiskLimitGB+2, 4)` presigned PUT URLs under `image-backups/server-<id>/<ts>/part-001.tar.gz`..., calls `wClient.UploadImageBackup` (passing `ContainerID` only when `status == running`), stores only the uploaded `PartKeys[:resp.PartsUploaded]`, logs `ServerOpImageBackup`, and prunes older completed `ServerImageBackup` records beyond `maxBackups` (deleting all part keys in S3 + DB row).
    - `ListImageBackups(ctx, serverID)` and `ValidateImageRestoreTarget(ctx, serverID, imageBackupID)`
    - `RestoreImageBackup(ctx, serverID, imageBackupID)`: mints presigned GET URLs for `PartKeys` in order, stops container if active, calls `wClient.RestoreImageBackup`, recreates/starts container if it was running, and logs `ServerOpImageRestore`.
  - [ ] Extend `internal/jobs/queue.go`:
    - `EnqueueImageBackup(ctx, userID, serverID)` (deduplicates if a `backup_image` job for the server is already `pending` or `running`).
    - `EnqueueImageRestore(ctx, userID, serverID, imageBackupID)`.
    - `EnqueueScheduledImageBackups(ctx)` targeting all servers with `status IN ('running', 'stopped')`.
    - `StartImageBackupCron(ctx, cronSpec string)` scheduling `EnqueueScheduledImageBackups` via `robfig/cron/v3` and stopping cleanly on context cancellation.
    - Dispatch `JobTypeBackupImage` and `JobTypeRestoreImage` in `executeJob` and reset `ServerImageBackup` to `pending` in `RetryJob`.
  - [ ] Wire `backupService.SetMaxBackups(cfg.BackupMaxCount)`, `backupService.SetPresignTTL(cfg.BackupPresignTTL)`, and `jobService.StartImageBackupCron(sigCtx, cfg.CronMCServerDataBackup)` in `cmd/jobworker/main.go` (and apply `SetMaxBackups` / `SetPresignTTL` in `cmd/apiserver/main.go`).
  - [ ] Remove dead `ScheduleBackups` / `SweepAndBackupAllActive` (and its unused `cronRunner` field) from `internal/backup/backup.go` once superseded by `jobs.Service.StartImageBackupCron`.
  - [ ] Add unit tests in `internal/backup/backup_test.go` and `internal/jobs/queue_test.go` covering CRON enqueueing of running+stopped servers (skipping pending/provisioning/migrating/error), job execution, multi-part pruning (8-backup limit), and image restore job execution.
- Done when: `go test ./...` and `go build ./cmd/...` pass, and starting `cmd/jobworker` logs the active `CRON_MC_SERVER_DATA_BACKUP` schedule, `BACKUP_MAX_COUNT`, and `BACKUP_PRESIGN_TTL`.

### Phase 8: Add admin image-backup endpoints on `cmd/apiserver` and restore shell script [STATUS: TODO]
- Goal: Expose admin-only HTTP endpoints on `cmd/apiserver` to list/trigger/restore ext4 image backups and provide a standalone shell script to invoke image restore.
- Tasks:
  - [ ] Add admin-only routes in `internal/backup/handler.go` (protected by `authkit.AuthMiddleware` + admin role guard):
    - `GET /api/v1/admin/servers/:id/image-backups` — list `ServerImageBackup` records for a server.
    - `POST /api/v1/admin/servers/:id/image-backups` — enqueue an immediate `JobTypeBackupImage` job (`202 Accepted`).
    - `POST /api/v1/admin/servers/:id/image-backups/:backup_id/restore` — enqueue a `JobTypeRestoreImage` job (`202 Accepted` with job ID).
  - [ ] Add executable shell script `scripts/restore-image-backup.sh` (`chmod +x`): accepts `<server_id> [image_backup_id]`, logs in or uses `ADMIN_TOKEN` against `APP_BASE_URL` (default `http://localhost:8080`), selects the latest completed image backup if `image_backup_id` is omitted, triggers `POST /api/v1/admin/servers/<server_id>/image-backups/<backup_id>/restore`, and polls `GET /api/v1/jobs/<job_id>` until completion or failure.
  - [ ] Add HTTP route tests in `internal/backup/backup_test.go` verifying admin access succeeds and non-admin users receive `403 Forbidden`.
  - [ ] Update `TODO.md` checkboxes for completed items.
- Done when: `go test ./...` passes, `shellcheck scripts/restore-image-backup.sh` (or `bash -n`) passes, and a full local Docker Compose run (`apiserver` + `jobworker` + `worker` + `seaweedfs`) verifies triggering an ext4 `.img` backup, seeing the uploaded `.tar.gz` part(s) in S3, pruning beyond 8 backups, and restoring via `scripts/restore-image-backup.sh`.

---

## Unknowns, risks, assumptions

- **S3 endpoint reachability from remote workers**: Presigned URLs are signed with `jobworker`'s `S3_ENDPOINT` (e.g. `http://seaweedfs:8333` in Compose or a real S3 endpoint in prod). Every `cmd/worker` node must be able to reach that exact hostname/port because S3 SigV4 binds the `Host` header. If workers later run on external VPS nodes behind a different hostname than `jobworker`, a separate `S3_PRESIGN_ENDPOINT` env var will be needed.
- **SeaweedFS SigV4 presigned URL behavior**: SeaweedFS S3 gateway supports SigV4 presigned PUT/GET with explicit `Content-Length`, which is why worker buffers each `<= 1 GiB` part to a temp file before PUT instead of chunked transfer encoding. Phase 2's SeaweedFS integration test should be run against the live dev SeaweedFS container early to confirm no header quirks.
- **Temporary disk space on worker nodes**: Buffering compressed `.tar.gz` parts locally requires up to `1 GiB` of free temp space per part (cleaned up immediately after each part uploads) and up to `disk_limit_gb` during atomic `.img` restore before `os.Rename`.
- **Live ext4 consistency**: `save-off` + `save-all` + `sync` avoids player downtime and flushes Minecraft world chunks, but a live rw-mounted ext4 filesystem can still have dirty journal metadata; on restore, running `e2fsck -p` (best-effort) before remounting is a low-cost safeguard to consider during Phase 5 implementation.
- **Pre-allocated presigned PUT URL count**: Pre-minting `max(disk_limit_gb + 2, 4)` presigned PUT URLs assumes gzipped tar of an ext4 image will not exceed `(disk_limit_gb + 2) GiB`; unused presigned URLs expire without creating S3 objects.
