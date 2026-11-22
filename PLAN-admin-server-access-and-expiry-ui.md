# Plan: Admin Server Access & Expiry Extension UI

Source: `TODO.md` lines 142-148.
Repo: Go (`internal/{domain,hosting,backup,jobs,billing,dashboard}`) with server-rendered Bootstrap HTML templates (`internal/dashboard/templates/`).
Build & test commands:
- `go vet ./...`
- `go test -p 1 ./...`
- `go build ./cmd/...`

## Phases

### Phase 1: Fix staff/admin access for server actions (dashboard & API) [STATUS: DONE]
- Goal: Allow staff and admin users to view and operate (backup, restore, send console commands, update settings, start/stop, delete) on another user's server without hitting `unauthorized access to server`.
- Tasks:
  - [x] Widen `effectiveServerUserID(c)` in `internal/dashboard/admin.go` from `IsAdmin()` to `IsStaffOrAdmin()` so both staff and admin users bypass ownership checks across dashboard server views and actions.
  - [x] Update dashboard action handlers in `internal/dashboard/dashboard.go` (`SendConsoleCommand`, `UpdateServerSettings`, `StartServer`, `StopServer`, `DeleteServer`, `TriggerBackup`, `RestoreBackup`) to use `s.effectiveServerUserID(c)` (matching existing read handlers in `dashboard.go` and `admin.go`).
  - [x] Add an equivalent staff/admin-aware helper (`IsStaffOrAdmin()`) in `internal/hosting/handler.go` and `internal/backup/handler.go` so authenticated staff and admin API requests to `/api/v1/servers/:id/...` (get, start, stop, settings, delete, console logs/command, backup, restore) also bypass ownership checks.
  - [x] Ensure `actorID` fallback (`if actorID == 0 { actorID = server.UserID }`) is applied consistently in `jobs.Service.EnqueueDeleteServer` (`internal/jobs/queue.go`) and `hosting.Service.{StopServer,SendServerCommand}` (`internal/hosting/service.go`) so job rows and `server_operation_logs` never store `user_id = 0` when staff/admin triggers an action.
  - [x] Add tests in `internal/dashboard/admin_test.go` (and/or `internal/dashboard/dashboard_test.go`) verifying both staff and admin can trigger backup, restore, send console command, update settings, start/stop, and delete another user's server while a regular non-owner user is still rejected.
- Done when: `go vet ./...`, `go test -p 1 ./...`, and `go build ./cmd/...` pass; tests cover staff and admin performing all listed server actions on another user's server and regular non-owner denial remains enforced.

### Phase 2: Fix staff/admin Manage Files access on another user's server [STATUS: DONE]
- Goal: Allow staff and admin users clicking "Manage Files" on another user's server to provision themselves on the node fileserver, receive directory permission for that server, and log in as themselves.
- Tasks:
  - [x] Update `hosting.Service.OpenFileManagerURL` in `internal/hosting/service.go` to accept the requesting `actorUserID` and an `allowNonOwner bool` (staff/admin) flag:
    - Bypass ownership check (`getServerAndHost(ctx, 0, serverID)`) when `allowNonOwner` is true; otherwise enforce ownership (`getServerAndHost(ctx, actorUserID, serverID)`).
    - Load `actorUserID` (not `server.UserID`) from `users` so `fsClient.EnsureUser`, `fsClient.Allow(ctx, actor.ID, server.ID)`, and `fsClient.CreateLoginToken(ctx, actor.ID)` grant and authenticate the requesting staff/admin user directly on the fileserver.
  - [x] Update `dashboard.Service.OpenFileManager` in `internal/dashboard/dashboard.go` to pass `userID` and `authkit.GetUserRoleFromContext(c).IsStaffOrAdmin()`.
  - [x] Update existing `TestOpenFileManagerURLAndRevokeOnDelete` in `internal/hosting/hosting_test.go` and add tests in `internal/hosting/hosting_test.go` and `internal/dashboard/admin_test.go` verifying:
    - Staff and admin opening Manage Files on another user's server call fileserver `EnsureUser`, `Allow`, and `CreateLoginToken` with the **requesting staff/admin's** user ID/email and the target `server.ID`.
    - Regular non-owner user is still rejected with `unauthorized access to server`.
- Done when: `go vet ./...`, `go test -p 1 ./...`, and `go build ./cmd/...` pass; mock fileserver assertions confirm staff/admin receives permission for the target server and redirects with a valid login token.

### Phase 3: Move Extend Server button to new panel in Server Settings page [STATUS: TODO]
- Goal: Relocate the "Extend 30 Days" action from the Server Overview page into a dedicated `#extend-server` panel on the Server Settings page.
- Tasks:
  - [ ] Update `dashboard.Service.ShowServerSettings` in `internal/dashboard/dashboard.go` to pass expiration/extension state (`IsExpired`, `CanExtend`, retention days) to `server_settings.html` (extracting a small shared helper for computing expiry/extend flags so `ShowServerDetail` and `ShowServerSettings` stay DRY).
  - [ ] Add a dedicated "Extend Server" card panel with `id="extend-server"` in `internal/dashboard/templates/server_settings.html` (between Software & Version and Danger Zone), showing current expiration status/timestamp and the `POST /dashboard/servers/{{.Server.ID}}/extend` form button when `CanExtend` is true.
  - [ ] Remove the inline `Extend 30 Days` button from `internal/dashboard/templates/server_detail.html`, replacing the expired-banner CTA with a link to `/dashboard/servers/{{.Server.ID}}/settings#extend-server`.
  - [ ] Update `dashboard.Service.ExtendServerDuration` error redirect to return to `/dashboard/servers/:id/settings#extend-server` (and update `billing.Service.CreateExtendCheckout` `CancelURL` to `/dashboard/servers/:id/settings#extend-server` if appropriate).
  - [ ] Add/update dashboard tests verifying the Extend Server panel renders on `/dashboard/servers/:id/settings` with `id="extend-server"` and the `Extend 30 Days` form is no longer rendered on `/dashboard/servers/:id`.
- Done when: `go vet ./...`, `go test -p 1 ./...`, and `go build ./cmd/...` pass; `/dashboard/servers/:id/settings#extend-server` displays the Extend Server panel and submits to `POST /dashboard/servers/:id/extend`.

### Phase 4: Add expiring-server alert on web dashboard with link to Extend Server panel [STATUS: TODO]
- Goal: Warn users on the main dashboard when any of their servers is about to expire (or already expired within retention) with a direct link to that server's Settings > Extend Server panel.
- Tasks:
  - [ ] Add `Server.IsExpiringSoon(now time.Time, window time.Duration) bool` on `domain.Server` in `internal/domain/models.go` (true when `ExpiresAt != nil && ExpiresAt.After(now) && ExpiresAt.Sub(now) <= window`) with unit tests in `internal/domain/db_test.go` (or `models_test.go`).
  - [ ] Define a named warning threshold constant in `internal/dashboard/dashboard.go` (e.g. `serverExpiringSoonWindow = 7 * 24 * time.Hour`) and compute `ExpiringServers` (expiring soon or expired within retention) in `ListServers`.
  - [ ] Render a Bootstrap alert banner at the top of `internal/dashboard/templates/servers_list.html` when `ExpiringServers` is non-empty, showing each server's name, expiry timestamp/status, and a link to `/dashboard/servers/{{.ID}}/settings#extend-server`.
  - [ ] Also show an expiring-soon warning notice on `internal/dashboard/templates/server_detail.html` linking to `/dashboard/servers/{{.Server.ID}}/settings#extend-server` when the server is about to expire.
  - [ ] Add tests in `internal/dashboard/dashboard_test.go` verifying the alert appears for servers expiring within the warning window (with link to `/dashboard/servers/<id>/settings#extend-server`), appears for expired-within-retention servers, and does not appear for servers with `ExpiresAt == nil` or expiring well beyond the window.
- Done when: `go vet ./...`, `go test -p 1 ./...`, and `go build ./cmd/...` pass; dashboard list and detail pages render the expiring alert with working `#extend-server` links; tick lines 142-148 in `TODO.md`.

## Unknowns, risks, assumptions

- **Role scope**: Cross-user server access (dashboard, API, and fileserver) is granted to both `staff` and `admin` via `IsStaffOrAdmin()`.
- **Assumption (expiring-soon window)**: Defaulting the "about to expire" alert threshold to **7 days** before `ExpiresAt` (constant `serverExpiringSoonWindow`), matching the 7-day retention window. Easy to adjust or make configurable if a different threshold (e.g., 3 days) is preferred.
- **Assumption (extend checkout ownership)**: `ExtendServerDuration` (`POST /dashboard/servers/:id/extend`) calls `billing.Service.CreateExtendCheckout`, which still enforces `server.UserID == userID` since Stripe billing orders belong to the server owner. Staff/admins are not expected to pay for another user's server via Stripe Checkout.
- **Risk (fileserver permission persistence)**: Granting staff/admin `Allow(actor.ID, server.ID)` on the node fileserver persists in fileserver SQLite until the server is deleted (`RevokeServer` clears all grants for that `server_id`). This matches the current fileserver permission model; if a staff/admin user is later demoted to `user`, existing fileserver permissions would need manual revocation unless a role-demotion hook is added later.
- **Risk (parallel DB tests)**: Always run multi-package tests with `go test -p 1 ./...` to avoid cross-package races on the shared local Postgres `server_jobs` table.
