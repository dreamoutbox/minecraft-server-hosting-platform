# authkit

Reusable authentication toolkit for Go (`gin` + `gorm`) supporting:

- **Database-backed session cookies** (`github.com/dreamoutbox/authkit/session`) for browser UIs
- **Bearer JWT authentication** (`github.com/dreamoutbox/authkit/authkitjwt`) for REST APIs and short-lived cross-service login handoffs

## Packages

- `github.com/dreamoutbox/authkit`: `SetupDB` and top-level re-exports of core user/password/auth helpers.
- `github.com/dreamoutbox/authkit/shared`: Shared `User`, `UserRole`, `Session` models, password hashing, context keys, `Authenticate`, and `RegisterUser`.
- `github.com/dreamoutbox/authkit/session`: `Manager`, `Store`, `CookieConfig`, `SetCookie`/`ClearCookie`, and `Middleware` for opaque `HttpOnly` session cookies stored as SHA-256 hashes in the `sessions` table.
- `github.com/dreamoutbox/authkit/authkitjwt`: JWT token minting, parsing/validation, single-use login handoff tokens, and `JWT_ACCESS_TTL` config.
- `github.com/dreamoutbox/authkit/middleware`: Bearer JWT `AuthMiddleware` (`middleware/auth.go`) and context getters `GetUserIDFromContext` / `GetUserRoleFromContext` (`middleware/getter.go`).
- `github.com/dreamoutbox/authkit/handler`: Modular REST API auth handlers (one file per route: `register.go`, `login.go`, `refresh.go`, `logout.go`, `get_profile.go`, `update_profile.go`).

## Database Setup

Run `authkit.SetupDB(db)` to create the `users` and `sessions` tables:

```go
if err := authkit.SetupDB(db); err != nil {
    log.Fatal(err)
}
```

If your application needs extra columns on `users`, embed `authkit.User` and pin the table name:

```go
type User struct {
    authkit.User
    CustomerID string `gorm:"type:varchar(128)" json:"customer_id,omitempty"`
}

func (User) TableName() string {
    return "users"
}
```

## Session Cookie Auth (Recommended for Browser UIs)

```go
cookieCfg := session.DefaultCookieConfig(session.DefaultCookieName, 24*time.Hour, isHTTPS)
authsessions := session.NewManager(db, cookieCfg)

// Register (user then logs in manually)
_, err := authkit.RegisterUser(db, email, password)

// Login
user, err := authkit.Authenticate(db, email, password)
if err == nil {
    if err := authsessions.SetSession(c, user.ID); err != nil {
        // handle error
    }
}

// Protect routes (redirect to /login when unauthenticated; pass "" for 401 JSON)
protected := r.Group("/dashboard")
protected.Use(authsessions.Middleware("/login"))

// Logout (revokes DB session + clears cookie with matching Secure/SameSite flags)
_ = authsessions.Logout(c)
```

`session.Middleware` loads the live `shared.User` from the database on each request, so role updates, account deletions, and logouts take effect immediately.

## Bearer JWT Auth (REST APIs)

```go
authHandler := handler.NewHandler(db, jwtSecret)
authHandler.RegisterRoutes(r.Group("/api/v1/auth"))

api := r.Group("/api/v1")
api.Use(middleware.AuthMiddleware(jwtSecret))
```

### Environment Variables

- `JWT_ACCESS_TTL`: Go duration string controlling default access JWT expiry (default: `15m`).

## Reading Identity in Handlers

Both `session.Middleware` and `middleware.AuthMiddleware` populate the same Gin context keys:

```go
userID, err := middleware.GetUserIDFromContext(c)
role := middleware.GetUserRoleFromContext(c)
```

## Examples

Runnable examples live in `examples/`:

- `examples/session/main.go` — Browser session-cookie auth with `github.com/dreamoutbox/authkit/session`
- `examples/jwt_api/main.go` — REST API Bearer JWT auth with `github.com/dreamoutbox/authkit/handler` and `github.com/dreamoutbox/authkit/middleware`

Run either example from `shared/authkit`:

```bash
go run ./examples/session
go run ./examples/jwt_api
```
