# Plan: Fileserver UID/GID Ownership & SQLite Inspect Script

Source: `fileserver/TODO-fileserver.md` lines 27-29 + user request (`itzg/minecraft-server` uses `1000:1000`, so `apiserver` must register fileserver users with `uid=1000, gid=1000`).
Modules:
- `fileserver/` — `cd fileserver && go vet ./... && go test ./... && go build ./cmd/...`
- repo root — `go vet ./... && go test -p 1 ./... && go build ./cmd/...`

## Phases

### Phase 1: Add `uid` and `gid` to fileserver `users` table, store, API, and client [STATUS: DONE]
- Goal: Persist optional Unix `uid` and `gid` per user in fileserver's SQLite `users` table and expose them through `PUT /api/v1/users` and `fileserver/client`.
- Tasks:
  - [x] Define `store.User` in `fileserver/internal/store/models.go` embedding `authkit.User` with `UID *int` (`gorm:"column:uid"`) and `GID *int` (`gorm:"column:gid"`) and explicit `TableName() string { return "users" }` (keeps `shared/authkit.User` untouched for the main Postgres DB while adding `uid`/`gid` columns to fileserver SQLite).
  - [x] Update `OpenSQLite`, `EnsureUser`, and `GetUser` in `fileserver/internal/store/store.go` to use `store.User`:
    - Validate that provided `uid`/`gid` are non-negative (`>= 0`).
    - When creating a user, store provided `uid`/`gid`.
    - When updating an existing user, update `uid`/`gid` if non-nil and changed (preserving existing values when nil).
  - [x] Extend `client.EnsureUserRequest` and `client.EnsureUserResponse` in `fileserver/client/types.go` with `UID *int` and `GID *int` (`json:"uid,omitempty"`, `json:"gid,omitempty"`), and update `client.Client.EnsureUser` / `HTTPClient.EnsureUser` in `fileserver/client/client.go`.
  - [x] Update `api.Handler.RegisterUser` in `fileserver/internal/api/handler.go` to reject negative `uid`/`gid` with HTTP 400, pass `req.UID`/`req.GID` to `store.EnsureUser`, and return them in `EnsureUserResponse`.
  - [x] Update existing callers in `internal/hosting/service.go` (temporary nil or 1000:1000? Pass `nil, nil` in Phase 1 so Phase 1 compiles across the workspace, then wire `1000:1000` in Phase 3) and add unit tests in `fileserver/internal/store/store_test.go` and `fileserver/internal/api/handler_test.go`.
- Done when: `cd fileserver && go vet ./... && go test ./... && go build ./cmd/...` and root `go vet ./... && go build ./cmd/...` pass; SQLite `users` table has `uid`/`gid` columns and `PUT /api/v1/users` persists and updates them.

### Phase 2: Apply uploader `uid:gid` ownership on file upload and creation in fileserver [STATUS: TODO]
- Goal: Ensure files uploaded (and files/folders created) through the fileserver web UI are owned by the logged-in user's configured `uid:gid` instead of `root:root`.
- Tasks:
  - [ ] Add a `FileOwner` helper struct (e.g. `UID *int`, `GID *int`) and a shared `applyFileOwnership(f *os.File, owner *FileOwner) error` helper in `fileserver/internal/files/fs.go` that calls `f.Chown(uid, gid)` on the open file descriptor inside `os.Root` (symlink/TOCTOU-safe) when `owner` specifies valid non-nil `UID`/`GID` (or falls back to `-1` for an unspecified half).
  - [ ] Update `SaveUploadedFile`, `CreateFile`, and `CreateDir` in `fileserver/internal/files/fs.go` to accept `*FileOwner` and apply ownership immediately upon creation (for `CreateDir`, open the newly created directory via `root.Open` and call `f.Chown`).
  - [ ] Update `files.Handler` (`Upload`, `CreateFile`, `MakeDir`) in `fileserver/internal/files/handler.go` to load the authenticated user's `UID`/`GID` from `h.store.GetUser(ctx, userID)` via a shared helper `h.resolveFileOwner(ctx, userID)` and pass it to `SaveUploadedFile`, `CreateFile`, and `CreateDir`.
  - [ ] Add tests in `fileserver/internal/files/fs_test.go` and `fileserver/internal/files/handler_test.go` verifying ownership is applied on upload, create file, and create folder (using `os.Getuid()`/`os.Getgid()` so tests pass cleanly as both non-root and root, plus testing the nil-owner no-op path).
- Done when: `cd fileserver && go vet ./... && go test ./... && go build ./cmd/...` passes; uploaded files, created files, and created folders get chowned to the user's `uid:gid` when configured.

### Phase 3: Register fileserver users with `1000:1000` from API server [STATUS: TODO]
- Goal: Have `apiserver` register users on the node fileserver with `uid=1000, gid=1000` to match the `itzg/minecraft-server` container user.
- Tasks:
  - [ ] Define named constants `defaultMinecraftFileUID = 1000` and `defaultMinecraftFileGID = 1000` in `internal/hosting/service.go`.
  - [ ] Update `hosting.Service.OpenFileManagerURL` in `internal/hosting/service.go` to pass `&uid, &gid` (`1000, 1000`) to `fsClient.EnsureUser`.
  - [ ] Update `TestOpenFileManagerURLAndRevokeOnDelete` in `internal/hosting/hosting_test.go` and `TestStaffAndAdminOpenFileManagerOnOtherUserServer` in `internal/dashboard/admin_test.go` to assert `EnsureUser` receives `uid=1000` and `gid=1000`.
- Done when: `go vet ./...`, `go test -p 1 ./...`, and `go build ./cmd/...` pass; mock fileserver assertions confirm `PUT /api/v1/users` includes `uid: 1000, gid: 1000`.

### Phase 4: Add dev shell script to inspect fileserver SQLite database [STATUS: TODO]
- Goal: Provide a standalone dev shell script that lists all tables and rows in the fileserver SQLite database for quick local debugging.
- Tasks:
  - [ ] Create `fileserver/dev-inspect-sqlite.sh` (`#!/usr/bin/env bash`, `set -euo pipefail`, `chmod +x`):
    - Accepts optional SQLite file path argument (or `--container <name>`, defaulting to `mc_fileserver` / `/data/fileserver.db` via `docker cp` into a temp dir cleaned up on `EXIT` trap when no local path is given).
    - Inspects the database using `sqlite3` (preferred, with `-header -column`) or a fallback helper if `sqlite3` is unavailable, printing every user table name, row count, and all rows.
  - [ ] Verify script syntax with `bash -n fileserver/dev-inspect-sqlite.sh` and `shellcheck fileserver/dev-inspect-sqlite.sh`, and run it against a temporary SQLite DB populated by `store.OpenSQLite` to confirm formatted output.
  - [ ] Tick lines 27 and 29 in `fileserver/TODO-fileserver.md`.
- Done when: `bash -n` and `shellcheck` pass, running `fileserver/dev-inspect-sqlite.sh <temp-db>` prints all tables (`users`, `dir_permissions`, `used_login_tokens`, `transfer_usages`) and rows cleanly, and `fileserver/TODO-fileserver.md` is updated.

## Unknowns, risks, assumptions

- **Assumption (container privilege)**: `fileserver/Dockerfile` runs as `root` inside `mc_fileserver`, which has `CAP_CHOWN` to chown uploaded files on the mounted ext4 volume to `1000:1000`. In non-root local dev runs, chowning to a different UID will fail with `EPERM`; unit tests will use `os.Getuid()`/`os.Getgid()` to test real `Chown` syscalls without requiring root.
- **Assumption (fixed 1000:1000 for itzg)**: `apiserver` sends `1000:1000` for all Minecraft servers since `itzg/minecraft-server` defaults to UID/GID `1000:1000`. If future server types use different UID/GIDs, the `fileserver` API already accepts per-user `uid`/`gid` values.
- **Assumption (existing files on edit)**: `SaveEditableFile` truncates and writes to an existing file in place (`O_WRONLY|O_TRUNC`), preserving its existing owner; `Upload`, `CreateFile`, and `CreateDir` create new inodes and will explicitly apply `uid:gid`.
- **Risk (host tooling for SQLite script)**: `mc_fileserver` uses `alpine:latest` without `sqlite3` installed, so `fileserver/dev-inspect-sqlite.sh` should copy `/data/fileserver.db` out of the container with `docker cp` and read it on the host with `sqlite3` (avoiding `docker exec ... sqlite3` failures and avoiding inline scripts).
