# Plan: fileserver (remaining TODO items)

Source: `TODO-fileserver.md` lines 21-26. Project: `fileserver/` (Go, gin, GORM/SQLite, server-rendered Bootstrap templates).
Test command: `cd fileserver && go test ./...` (existing tests in `internal/{files,store,config,api}` and `cmd/fileserver`).

Order: independent UI items first, then the quota feature (backend + store + config), since quota touches upload/download handlers that the earlier phases also modify.

### Phase 1: Immediate browser file dialog for upload [STATUS: DONE]
- Goal: "Upload File" button opens the native file picker and submits on selection, with no modal.
- Tasks:
  - [x] In `internal/files/templates/files.html`, replace the `#uploadFileModal` trigger button (line ~31) with a button that clicks a hidden `<input type="file" name="file">` inside a hidden form posting to `/upload` (multipart, with `path` field = current dir, as the modal form does today).
  - [x] Auto-submit the form on the input's `change` event (small inline-free JS block in the existing script section).
  - [x] Remove the `#uploadFileModal` markup (lines ~96-117).
  - [x] Keep `Upload` handler in `internal/files/handler.go` unchanged; confirm field order (`path` before `file`) is preserved since it uses `MultipartReader`.
- Done when: `go test ./...` passes; manual check: clicking Upload File opens the OS dialog, picking a file uploads it and the list refreshes with the success message; no upload modal in the DOM.

### Phase 2: Multi-select checkboxes [STATUS: DONE]
- Goal: select multiple entries via checkboxes and act on them in bulk.
- Tasks:
  - [x] Add a checkbox column plus a select-all header checkbox to the table in `files.html`.
  - [x] Add a bulk action bar in the sub-navbar toolbar (shown only when 1+ selected): Delete selected, Download selected as zip. (Assumption: these are the bulk actions wanted.)
  - [x] Backend: add `POST /delete-multi` (accepts repeated `paths` fields) reusing `authorizePath` and the single-delete logic in `Delete`; extract the shared delete routine into a helper in `internal/files/` rather than copying.
  - [x] Backend: add `POST /download-zip-multi` building a zip of selected entries; extend `OpenDirForZip` logic in `fs.go` with a multi-path variant, sharing the zip-walk code.
  - [x] Register routes in `RegisterRoutes`.
  - [x] Tests in `handler_test.go` / `fs_test.go`: multi-delete, multi-zip, rejection of paths outside the permitted dir (path traversal), empty selection.
- Done when: `go test ./...` passes including the new tests; manual check: select-all, partial selection, bulk delete and bulk zip work and the bar hides when nothing is selected.

### Phase 3: Quota config and usage store [STATUS: DONE]
- Goal: persist per-user daily transfer usage and expose the configurable limit.
- Tasks:
  - [x] `internal/config/config.go`: add `FILESERVER_DAILY_QUOTA_MB` (default 5120 = 5GB, `0`/unset-to-default semantics decided below), parsed with the existing `parsePositiveInt` pattern; add `DailyQuotaBytes` to `Config`. Use a named constant for the default.
  - [x] `internal/store/models.go`: add `TransferUsage` model (`UserID`, `Day` date string `YYYY-MM-DD` in UTC, `UploadBytes`, `DownloadBytes`; unique index on user+day); add to AutoMigrate in `OpenSQLite`.
  - [x] `internal/store/store.go`: add `GetUsage(ctx, userID, day)`, and `AddUsage(ctx, userID, day, direction, bytes)` using an atomic upsert (`ON CONFLICT DO UPDATE`). Use a `TransferDirection` enum (upload/download).
  - [x] Add cleanup of rows older than N days (reuse the pattern used for expired `UsedLoginToken`, if present).
  - [x] Tests: `config_test.go` (default, override, invalid) and `store_test.go` (upsert accumulates, day rollover isolates).
  - [x] Add env var to `docker-compose.app.yml` (`FILESERVER_DAILY_QUOTA_MB`), `template.env`, and `docs/fileserver-deployment.md`.
- Done when: `go test ./...` passes; no handler behavior change yet; env var documented.

### Phase 4: Enforce quota on upload and download [STATUS: DONE]
- Goal: block transfers beyond the daily limit and record usage.
- Tasks:
  - [x] Pass `DailyQuotaBytes` into `files.NewHandler` (update `cmd/fileserver/main.go` and test constructors).
  - [x] Add a shared helper in `internal/files/` (`checkQuota` / `recordUsage`) used by `Upload`, `Download`, `DownloadZip`, and the Phase 2 multi-zip route.
  - [x] Upload: reject up front using `Content-Length` when it exceeds remaining quota; record actual bytes written after the copy completes (a streamed/chunked upload without `Content-Length` is capped by wrapping the body reader with the remaining quota).
  - [x] Download: check remaining quota before sending; for single files use the file size; for zips count bytes actually streamed via a counting writer, since the final size is unknown up front.
  - [x] Return HTTP 429 with a clear message (error page via `renderError` or redirect message) when exceeded.
  - [x] Show remaining/used quota in the page header (`Browse` passes it to the template).
  - [x] Tests in `handler_test.go`: under-quota succeeds and records bytes; over-quota upload and download rejected with 429; next-day reset; usage counted per user.
  - [x] Update `docs/fileserver-deployment.md` with behavior description.
- Done when: `go test ./...` passes; manual check with `FILESERVER_DAILY_QUOTA_MB=1`: a 2MB upload is rejected, downloads stop after the limit, usage shows in the UI; tick the three items in `TODO-fileserver.md`.

## Unknowns, risks, assumptions
- Assumption: quota is per user, per UTC day, with upload and download counted separately, each limited to 5GB. Alternatives: combined total, or per server directory. Say if you want different.
- Assumption: bulk actions for checkboxes are delete and zip download only.
- Risk: zip downloads have unknown size up front, so enforcement is mid-stream (truncated download) or approximate; the plan counts actual bytes and checks before starting only.
- Risk: the existing `Upload` handler streams multipart and is sensitive to field order (see `post-mortem/2026-11-18-fileserver-large-upload-slowdown.md`); do not buffer uploads when adding quota.
- Unknown: whether `0` should mean unlimited; proposed: yes, via a separate parse allowing zero.
