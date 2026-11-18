# Postmortem: Fileserver Slowdown During Large File Uploads

**Date:** 2026-11-18
**Severity:** Medium
**Status:** Resolved

## Summary
Opening or browsing `fileserver` in a browser became slow while a 1.1 GB file upload was in progress. Although `fileserver` handles HTTP requests concurrently per goroutine, its `POST /upload` handler buffered and wrote the entire upload to `/tmp` via `ParseMultipartForm` before copying it a second time to the target server mount, saturating disk I/O and causing latency for concurrent requests.

## Impact
- Browsing and file operations on `mc_fileserver` experienced noticeable latency while a 1.1 GB upload ran concurrently.
- Observed `mc_fileserver` container stats during investigation: `255.8 MiB` RSS memory and `469 MB / 12.9 GB` block I/O.
- Prior to the partial-file cleanup fix, failed uploads hitting `no space left on device` (`ENOSPC`) also left partial files consuming the remaining space on 1 GB server ext4 loop volumes.

## Timeline
- `20:23` — Raised `FILESERVER_MAX_UPLOAD_MB` default/compose limit from `100` to `2048` and added partial-file removal on `SaveUploadedFile` write/close errors so 1.1 GB uploads reach and fail cleanly on 1 GB ext4 mounts.
- `20:32` — User reported `fileserver` browser loading became slow while a large test upload was running and asked whether `fileserver` was single-threaded.
- `20:33` — Identified `c.PostForm("path")` + `c.FormFile("file")` multipart buffering and double disk copy in `fileserver/internal/files/handler.go`; rewrote `Upload` to stream directly via `c.Request.MultipartReader()`.
- `20:34` — Verified concurrent browsing during an open upload stream with `TestStreamingUploadConcurrencyAndOutOfOrderFields` and rebuilt/restarted `mc_fileserver`.
- `20:37` — Secondary live `tests/test-disk-limit.sh` run hit `mount -o loop ... exit status 32` because Docker Desktop WSL2 bind mounts held deleted `2.img`–`6.img` on `/dev/loop3`–`/dev/loop7`, exhausting the 8 mapped loop devices until `dev-cleanup-wsl-mounts.sh` is run.

## Root Cause
1. **Buffered multipart parsing & double disk write (`fileserver/internal/files/handler.go`):** Calling `c.PostForm("path")` triggered `http.Request.ParseMultipartForm` (32 MB RAM buffer), which read the entire upload payload and spilled it to `/tmp/multipart-*` in container overlayfs before `authorizePath` ran and before `SaveUploadedFile` copied the payload a second time to `/var/lib/mc-platform/mounts/<id>`. This doubled disk write traffic and saturated WSL2/Docker block I/O.
2. **Active-tab form submission:** Submitting `<form method="POST" action="/upload">` in `fileserver/internal/files/templates/files.html` blocks the submitting browser tab until the upload response returns.
3. **Related operational finding (WSL2 loop exhaustion):** Docker Desktop WSL2 caches bind mounts under `/mnt/wsl/docker-desktop-bind-mounts/<distro>/` pointing to `/dev/loop*`, keeping loop slots pinned after server deletion (`2.img (deleted)`–`6.img (deleted)`) unless cleaned up on the host.

## Detection
User observation during live large-file (`1.1 GB`) upload testing, confirmed via inspection of `fileserver/internal/files/handler.go`, `fileserver/cmd/fileserver/main.go`, `fileserver/internal/store/store.go`, and `docker stats mc_fileserver`.

## Resolution
- Rewrote `Handler.Upload` in `fileserver/internal/files/handler.go` to use `c.Request.MultipartReader()` so directory authorization runs as soon as `path` is parsed and the `file` stream writes once directly to the destination mount via `SaveUploadedFile` (with a temp-file fallback only when a client sends `file` before `path`).
- Updated `SaveUploadedFile` in `fileserver/internal/files/fs.go` to close and remove partial destination files when `io.Copy` or `dst.Close` fails.
- Set `ReadHeaderTimeout: 10 * time.Second` on `http.Server` in `fileserver/cmd/fileserver/main.go`.
- Added unit and concurrency tests (`TestStreamingUploadConcurrencyAndOutOfOrderFields` in `fileserver/internal/files/handler_test.go` and `TestSaveUploadedFileRemovesPartialOnError` in `fileserver/internal/files/fs_test.go`).

## Lessons Learned / Action Items
- [x] Stream multipart uploads in `fileserver` instead of buffering via `ParseMultipartForm` / `FormFile`.
- [x] Clean up partial uploaded files on write errors (`ENOSPC`).
- [ ] Run `./dev-cleanup-wsl-mounts.sh` before provisioning additional servers on Docker Desktop WSL2 after multiple deletions, or document loop device limit (`loop0`–`loop7`) cleanup in dev docs.
- [ ] Optionally switch the browser upload modal to `fetch` / `XMLHttpRequest` with progress indicator if background non-blocking uploads within the same tab are desired.
