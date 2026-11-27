# Plan: Authkit Session Auth & Cleanup

### Phase 1: Decouple `authkit.User`, add `authkit` DB setup, and fix `Register` DB error check [STATUS: DONE]
- Goal: Remove platform-specific billing columns from `shared/authkit`, provide an `authkit` migration helper, and fix the error-handling bug in `Handler.Register`.
- Tasks:
  - [x] Remove `CustomerID` and `PaymentMethodID` from `authkit.User` in `shared/authkit/model.go`.
  - [x] Add `AutoMigrate(db *gorm.DB) error` in `shared/authkit` to create the `users` table (and prepare a single entry point for authkit table setup).
  - [x] Change `domain.User` in `internal/domain/models.go` from `type User = authkit.User` to a struct embedding `authkit.User` with `CustomerID`, `PaymentMethodID`, and `TableName() string { return "users" }` (matching `fileserver/internal/store/models.go`).
  - [x] Update `domain.User{...}` composite literals across `internal/` (`internal/domain/db.go`, `internal/domain/db_test.go`, `internal/dashboard/dashboard.go`, `internal/dashboard/dashboard_test.go`, `internal/dashboard/admin_test.go`, `internal/hosting/hosting_test.go`, `internal/hosting/migration_test.go`, `internal/backup/backup_test.go`, `internal/billing/billing_test.go`, `internal/jobs/queue_test.go`).
  - [x] Fix `Handler.Register` in `shared/authkit/handler.go` to check `errors.Is(err, gorm.ErrRecordNotFound)` so database errors return `500` instead of falling through to user creation.
- Done when: `go test ./...` passes in `.`, `shared/authkit`, and `fileserver`; `authkit.User` has no billing fields while `domain.User` preserves `customer_id` and `payment_method_id` on the `users` table.

### Phase 2: Make access JWT expiry configurable via ENV [STATUS: DONE]
- Goal: Replace the hardcoded 15-minute access JWT TTL with an environment-configurable duration defaulting to `15m`.
- Tasks:
  - [x] Update `shared/authkit/token.go` to support configurable access token TTL (e.g., `DefaultAccessTokenTTL = 15 * time.Minute`, `GenerateTokenPairWithTTL`, and/or `AccessTokenTTLFromEnv()` reading `JWT_ACCESS_TTL` with `15m` fallback) while preserving `15m` default behavior.
  - [x] Add `JWTAccessTTL time.Duration` to `internal/config/config.go` via `parseEnvDuration("JWT_ACCESS_TTL", 15*time.Minute)` with positive-duration validation.
  - [x] Wire `cfg.JWTAccessTTL` into `authkit.Handler` in `cmd/apiserver/main.go`.
  - [x] Add unit tests in `shared/authkit/authkit_test.go` and `internal/config` covering default `15m`, custom valid durations, and invalid/non-positive values.
- Done when: `go test ./...` passes across all modules; setting `JWT_ACCESS_TTL` changes the `exp` claim on issued access JWTs, and leaving it unset defaults to 15 minutes.

### Phase 3: Implement `github.com/dreamoutbox/authkit/session` package and `sessions` table [STATUS: DONE]
- Goal: Build a DB-backed opaque session cookie package importable as `"github.com/dreamoutbox/authkit/session"` with secure cookie handling.
- Tasks:
  - [x] Create `shared/authkit/session/session.go` defining the `Session` GORM model (`TokenHash` primary key storing SHA-256 of the opaque random cookie token, `UserID` indexed, `ExpiresAt` indexed, `CreatedAt`, `LastSeenAt`).
  - [x] Create `shared/authkit/session/store.go` with `AutoMigrate(db *gorm.DB) error` (creating the `sessions` table) and `Store` methods (`Create`, `GetValid`, `Delete`, `DeleteByUserID`, `PruneExpired`); wire `sessions` migration into `authkit.AutoMigrate` (or call `session.AutoMigrate` from authkit's store setup function).
  - [x] Create `shared/authkit/session/cookie.go` with `CookieConfig` (`Name`, `MaxAgeSeconds`, `Secure`, `SameSite`, `Path`, `Domain`) and `SetCookie` / `ClearCookie` helpers (`HttpOnly: true`, `SameSite=Lax` by default, and matching `Secure` flag on both set and clear).
  - [x] Create `shared/authkit/session/middleware.go` with `Middleware(store *Store, cfg CookieConfig, loginPath string) gin.HandlerFunc` that validates the cookie against `sessions`, loads the current `authkit.User` from DB (ensuring immediate revocation and role updates), sets `authkit.ContextUserIDKey` / `ContextEmailKey` / `ContextRoleKey`, and clears invalid/expired cookies before redirecting or returning `401`.
  - [x] Add unit tests in `shared/authkit/session/session_test.go` covering session creation, validation, expiration, server-side logout revocation, live role reflection, and cookie `HttpOnly`/`SameSite`/`Secure` attributes on both set and clear.
- Done when: `go test ./...` in `shared/authkit` passes and external packages can import `"github.com/dreamoutbox/authkit/session"`.

### Phase 4: Migrate `dashboard` to `authkit/session` and secure cookie config [STATUS: TODO]
- Goal: Switch `internal/dashboard` from JWT-in-cookie to DB-backed sessions, remove hardcoded `secure = false`, and fix the ignored error in `ProcessRegister`.
- Tasks:
  - [ ] Update `internal/domain/db.go` `AutoMigrate` to run `session.AutoMigrate(db)` (or `authkit.AutoMigrate(db)`) so the `sessions` table is created in PostgreSQL.
  - [ ] Add `CookieSecure bool` (from `COOKIE_SECURE`, defaulting to `true` when `APP_BASE_URL` uses `https://` and `false` for local `http://`) and `SessionTTL time.Duration` (from `SESSION_TTL`, defaulting to `24h`) to `internal/config/config.go`.
  - [ ] Update `internal/dashboard/dashboard.go` and `cmd/apiserver/main.go` to inject `*session.Store` and `session.CookieConfig` into `dashboard.Service`.
  - [ ] Update `ProcessLogin` and `ProcessRegister` in `internal/dashboard/dashboard.go` to create a DB session and call `session.SetCookie`, handling any session-creation error in `ProcessRegister` instead of ignoring it.
  - [ ] Update `Logout` in `internal/dashboard/dashboard.go` to revoke the session in `session.Store` and call `session.ClearCookie` with the configured `CookieConfig`.
  - [ ] Replace `authkit.CookieAuthMiddleware` with `session.Middleware` in `internal/dashboard/dashboard.go`.
  - [ ] Update `internal/dashboard/dashboard_test.go` and `internal/dashboard/admin_test.go` to authenticate via `session.Store` cookies.
- Done when: `go test ./...` passes in `.`; dashboard login persists a session row, logout deletes it, expired sessions redirect to `/login`, and cookie `Secure` flag reflects config.

### Phase 5: Migrate `fileserver` browser UI to `authkit/session` and remove legacy cookie-JWT helpers [STATUS: TODO]
- Goal: Switch `fileserver` browser sessions to `authkit/session` while keeping one-time login JWTs for cross-service handoff, and remove obsolete JWT-cookie helpers from `shared/authkit`.
- Tasks:
  - [ ] Update `fileserver/internal/store/store.go` `OpenSQLite` to call `session.AutoMigrate(db)` so SQLite creates the `sessions` table.
  - [ ] Add `SessionTTL time.Duration` (from `FILESERVER_SESSION_TTL`, defaulting to `24h`) to `fileserver/internal/config/config.go` and wire `FILESERVER_COOKIE_SECURE` into `session.CookieConfig`.
  - [ ] Update `fileserver/internal/files/handler.go` and `fileserver/cmd/fileserver/main.go`:
    - In `LoginWithToken`, after consuming the one-time `TokenTypeLogin` JWT, create a DB session via `session.Store` and call `session.SetCookie`.
    - In `Logout`, delete the session from `session.Store` and call `session.ClearCookie` with matching `CookieConfig`.
    - Replace `authkit.CookieAuthMiddleware` with `session.Middleware`.
  - [ ] Update `fileserver` tests to use `session.Store` cookies.
  - [ ] Remove the now-unused `CookieAuthMiddleware`, `SetSessionCookie`, and `ClearSessionCookie` from `shared/authkit/middleware.go` and their corresponding tests from `shared/authkit/authkit_test.go`.
- Done when: `go test ./...` and `go build ./...` pass across `.`, `shared/authkit`, and `fileserver`; no caller uses JWT-in-cookie middleware anymore.

## Unknowns, risks, assumptions
- **Import path for future JWT package**: `shared/authkit/go.mod` declares module `github.com/dreamoutbox/authkit`, so `"github.com/dreamoutbox/authkit/session"` works out of the box as a subpackage. A future JWT package under the same module would be `"github.com/dreamoutbox/authkit/jwt"` (or a separate Go module if you specifically want `"github.com/dreamoutbox/jwt"`).
- **REST API auth unchanged**: `/api/v1/*` routes (`auth`, `hosting`, `billing`, `backup`, `jobs`) continue using Bearer JWT (`authkit.AuthMiddleware`) for now, with the configurable `JWT_ACCESS_TTL` from Phase 2.
- **Fileserver handoff unchanged**: Dashboard -> `fileserver` login continues using single-use `TokenTypeLogin` JWTs (`GenerateLoginToken`), which `fileserver` exchanges for a DB-backed `authkit/session` cookie.
- **Cookie `Secure` default in local dev**: Browsers reject `Secure` cookies over plain `http://` (except some browsers on `localhost`). Defaulting `COOKIE_SECURE` from `strings.HasPrefix(APP_BASE_URL, "https://")` with an explicit `COOKIE_SECURE=true/false` override follows web security practice without breaking local HTTP dev.
- **Session cleanup**: `Store.PruneExpired` will be provided in Phase 3; wiring a periodic background cleanup job/cron is deferred unless you want it included now.
