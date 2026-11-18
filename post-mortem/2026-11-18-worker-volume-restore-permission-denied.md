# Postmortem: Worker Volume Backup and Restore Permission Denied

**Date:** 2026-11-18
**Severity:** High
**Status:** Resolved

## Summary
Backup export silently produced truncated archives and backup restore failed with HTTP 500 `permission denied` on `mc_worker` for Minecraft server volumes (`sv1`). The root cause was `cap_drop: ALL` in `docker-compose.app.yml` stripping `CAP_DAC_OVERRIDE`, `CAP_CHOWN`, and `CAP_FOWNER` from `mc_worker`, combined with `ImportVolume` ignoring cleanup errors and `ExportVolume` walking ext4 `lost+found` and closing the tar writer on failure.

## Impact
- Server backup creation completed with HTTP 200 and recorded `status=completed`, while the stored archive (`backup_records.id=1`, `139890764` bytes) was truncated mid-stream when `ExportVolume` hit `lost+found` and omitted `paper-26.3-255.jar`, `plugins`, `server.properties`, `world`, and other entries alphabetically after `lost+found`.
- Backup restore failed on every attempt (`POST /volumes/1/import` returning HTTP 500) with `open /var/lib/mc-platform/mounts/1/.paper/version_history.json: permission denied` (and earlier on `.install-vanilla.env`).

## Timeline (UTC+7)
- `19:32` — Volume `1` created and mounted; `itzg/minecraft-server` chowned `/var/lib/mc-platform/mounts/1` and its contents to UID/GID `1000:1000` (`lost+found` mode `0700`, mount root `0755`, `.paper` `0775`, `version_history.json` `0664`).
- `19:33:26` — Backup triggered on `sv1`. `GET /volumes/1/export` started streaming HTTP 200 tar headers, hit `permission denied` on `lost+found`, logged `Headers were already written. Wanted to override status code 200 with 500`, and closed the tar writer cleanly via `defer`.
- `19:33:48`–`19:34:05` — Restore triggered; `POST /volumes/1/import` failed 3 times with HTTP 500 `permission denied` on `.paper/version_history.json`.
- `19:35` — User reported the restore error.
- `19:38` — Fix implemented and verified with `go test -v ./internal/worker/...`, `go build ./...`, and `docker compose ... config --quiet`.

## Root Cause
1. **Missing Linux capabilities on `mc_worker`**: `docker-compose.app.yml` configured `cap_drop: ALL` with only `cap_add: SYS_ADMIN` (`CapEff: 0000000000200000`). Without `DAC_OVERRIDE`, `CHOWN`, and `FOWNER`, root (`uid=0`) inside `mc_worker` was treated as world/other (`r-x` / `---`) against files owned by `1000:1000` (`itzg/minecraft-server`).
2. **Swallowed cleanup errors in `ImportVolume`**: `internal/worker/volume.go` called `_ = os.RemoveAll(...)` while cleaning `mountDir`, hiding the initial `permission denied` failure and leaving existing `1000:1000` files in place before extraction failed on `os.OpenFile`.
3. **Unsafe `lost+found` handling and premature `tw.Close()` in `ExportVolume`**: `filepath.Walk` descended into ext4's `lost+found` directory (`0700`), aborted mid-archive on `permission denied`, and `defer tw.Close()` still wrote a valid tar EOF trailer to an already-started HTTP 200 response.

## Detection
- User report of the restore failure UI/API error message on `sv1`.
- Inspection of `/var/lib/mc-platform/mounts/1` ownership, `mc_worker` `/proc/1/status` capability mask, and `mc_worker` container logs showing the truncated HTTP 200 `GET /volumes/1/export` warning.

## Resolution
- **`docker-compose.app.yml`**: Added `DAC_OVERRIDE`, `CHOWN`, and `FOWNER` alongside `SYS_ADMIN` under `worker.cap_add`.
- **`internal/worker/volume.go`**:
  - Added `isLostAndFoundRelPath` so `ExportVolume` (`filepath.SkipDir`) and `ImportVolume` skip ext4 `lost+found` and preserve it on disk.
  - Changed `ExportVolume` to call `tw.Close()` only after `filepath.Walk` succeeds, and added symlink handling.
  - Checked `os.ReadDir` and `os.RemoveAll` errors during `ImportVolume` cleanup instead of discarding them, and restored tar `Uid`/`Gid` ownership when running as root (`applyTarOwnership`).
- **`internal/worker/handler.go`**: Checked `c.Writer.Written()` before writing JSON error responses in `ExportVolume` so mid-stream failures do not append JSON into the tar body.
- **`internal/worker/worker_test.go`**: Added `TestExportAndImportSkipLostAndFoundAndCheckCleanup`.

## Lessons Learned / Action Items
- [ ] Rebuild and restart `mc_worker` from the updated `docker-compose.app.yml` and worker image, then discard truncated backup `#1` and create a fresh backup before restoring `sv1`.
- [ ] Mark the corresponding restore bug item in `TODO.md` complete once live backup and restore are verified in Compose.
- [ ] Add an end-to-end backup/restore integration check against a volume with non-root (`1000:1000`) ownership and a `0700` `lost+found` directory.
