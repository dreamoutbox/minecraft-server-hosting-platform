# Extract `go-stripe-kit` Module, Split Platform Billing, and Add Refunds

Extract reusable Stripe payment/webhook/refund logic out of `internal/billing` into a standalone Go module at `./go-stripe-kit` (with a separate Gin HTTP adapter at `./go-stripe-kit/ginapi` so other frameworks such as Echo or Fiber can be added later), move Minecraft server hosting platform billing logic and API routes into `./internal/billings`, and add full/partial refund support to `./go-stripe-kit`.

## Target Directory Layout

```
go-stripe-kit/                  # standalone module: github.com/dreamoutbox/go-stripe-kit
  go.mod
  go.sum
  README.md
  payment_status.go             # PaymentStatus enum (pending, succeeded, failed, refunded)
  event_type.go                 # EventType enum (checkout.session.completed, payment_intent.*, charge.refunded)
  refund_reason.go              # RefundReason enum (duplicate, fraudulent, requested_by_customer)
  refund_status.go              # RefundStatus enum (pending, succeeded, failed, canceled)
  payment_result.go             # PaymentResult
  checkout_session.go           # CheckoutSessionRequest, CheckoutSessionResult
  refund.go                     # RefundRequest (with validation), RefundResult
  webhook_event.go              # WebhookEvent (generic metadata, no MC order fields)
  webhook_handler.go            # WebhookHandler, RefundHook callback types
  provider.go                   # PaymentProvider interface
  stripe_provider.go            # StripePaymentProvider (stripe-go/v81, instance-scoped client)
  mock_provider.go              # MockPaymentProvider (in-memory payments/refunds + HMAC webhook helper)
  service.go                    # Framework-agnostic core Service
  provider_test.go              # Provider + refund + webhook signature unit tests
  service_test.go               # Core Service unit tests
  ginapi/                       # Gin HTTP adapter (thin transport layer only)
    handler.go                  # BillingService interface, Handler, RegisterWebhookRoute, RegisterRefundRoute
    webhook.go                  # POST webhook Gin handler
    refund.go                   # POST refund Gin handler
    handler_test.go             # Gin httptest unit tests

internal/billings/              # Minecraft server hosting platform billing
  service.go                    # Platform Service (wraps *stripekit.Service, *gorm.DB, JobEnqueuer)
  checkout.go                   # CreateServerCheckout, CreateExtendCheckout
  webhook_fulfillment.go        # HandleWebhookEvent (order claim, PaymentRecord, server create/extend, refund sync)
  queries.go                    # ListUserOrders, ListUserPayments
  api_handler.go                # /api/v1/billing route wiring (delegates webhook to ginapi)
  list_orders_api.go            # GET /api/v1/billing/orders
  list_payments_api.go          # GET /api/v1/billing/payments
  handler.go                    # Dashboard Handler (updated to local *Service)
  buy_server.go                 # GET/POST /dashboard/servers/buy
  extend_server.go              # POST /dashboard/servers/:id/extend
  show_billing.go               # GET /dashboard/billing
  service_test.go               # Migrated DB-backed integration tests
```

---

### Phase 1: Scaffold `./go-stripe-kit` module, core types, and workspace/Docker wiring [STATUS: DONE]
- Goal: Create the standalone `github.com/dreamoutbox/go-stripe-kit` module with framework-agnostic enums, request/result types, and the `PaymentProvider` interface, wired into `go.work`, root `go.mod`, and Dockerfiles.
- Tasks:
  - [x] Create `go-stripe-kit/go.mod` (`module github.com/dreamoutbox/go-stripe-kit`, Go `1.27.1`) with core dependencies (`github.com/stripe/stripe-go/v81`, `github.com/google/uuid`).
  - [x] Define core enums and structs (one type per file) in `go-stripe-kit/` (`package stripekit`):
    - `payment_status.go` (`PaymentStatus`: `pending`, `succeeded`, `failed`, `refunded`)
    - `event_type.go` (`EventType`: `checkout.session.completed`, `payment_intent.succeeded`, `payment_intent.payment_failed`, `charge.refunded`)
    - `refund_reason.go` (`RefundReason`: `duplicate`, `fraudulent`, `requested_by_customer` + `IsValid()`)
    - `refund_status.go` (`RefundStatus`: `pending`, `succeeded`, `failed`, `canceled`)
    - `payment_result.go` (`PaymentResult`)
    - `checkout_session.go` (`CheckoutSessionRequest` with generic `ClientReferenceID`, `Currency`, `CustomerEmail`, `ProductName`, `Description`, `AmountCents`, `SuccessURL`, `CancelURL`, `Metadata`; and `CheckoutSessionResult`)
    - `refund.go` (`RefundRequest` with `PaymentID`, `AmountCents` [0 = full refund, >0 = partial], `Reason`, `Metadata`, `IdempotencyKey`, and `Validate()`; and `RefundResult` with `RefundID`, `PaymentID`, `AmountCents`, `Currency`, `Status`, `Reason`)
    - `webhook_event.go` (`WebhookEvent` with `Type EventType`, `PaymentID`, `SessionID`, `RefundID`, `ClientReferenceID`, `AmountCents`, `Currency`, `CustomerID`, `Status PaymentStatus`, `Metadata map[string]string`)
    - `provider.go` (`PaymentProvider` interface: `CreatePayment`, `GetPayment`, `Refund`, `CreateCheckoutSession`, `VerifyWebhookSignature`)
  - [x] Register `./go-stripe-kit` in `go.work` (`use` and `replace github.com/dreamoutbox/go-stripe-kit v0.0.0 => ./go-stripe-kit`) and add `require` + `replace` in root `go.mod` (matching `./fileserver`).
  - [x] Update `Dockerfile.apiserver`, `Dockerfile.jobworker`, `Dockerfile.nodemanager`, and `fileserver/Dockerfile` to `COPY go-stripe-kit/go.* ./go-stripe-kit/` before `go mod download` / `go build`.
- Done when: `go build ./go-stripe-kit/...` and `go build ./...` pass, and `go list -m github.com/dreamoutbox/go-stripe-kit` resolves to `./go-stripe-kit`.

### Phase 2: Implement `StripePaymentProvider`, `MockPaymentProvider`, and refund operations [STATUS: DONE]
- Goal: Implement live Stripe and in-memory mock providers with full/partial refund support and generic webhook verification, verified by standalone unit tests.
- Tasks:
  - [x] Create `go-stripe-kit/stripe_provider.go` (`StripePaymentProvider` using an instance-scoped `stripe-go/v81` API client rather than mutating global `stripe.Key`):
    - Implement `CreatePayment`, `GetPayment`, and `CreateCheckoutSession` (default currency `usd`, forwarding `ClientReferenceID` and arbitrary `Metadata`).
    - Implement `Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)` supporting full refunds (`AmountCents == 0`), partial refunds (`AmountCents > 0`), `Reason`, `Metadata`, and `IdempotencyKey`, mapping Stripe refund statuses to `RefundStatus`.
    - Implement `VerifyWebhookSignature` handling `checkout.session.completed`, `payment_intent.succeeded`, `payment_intent.payment_failed`, and `charge.refunded`, copying Stripe object `metadata` generically without app-specific fields.
  - [x] Create `go-stripe-kit/mock_provider.go` (`MockPaymentProvider`):
    - Track created payments and cumulative refunded amounts in memory.
    - Implement `Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)` with full and partial refund support, rejecting unknown payments, negative amounts, over-refunds, invalid reasons, and already fully refunded payments; transition payment status to `PaymentStatusRefunded` once fully refunded.
    - Implement `CreateCheckoutSession`, `GenerateSignedWebhook`, and `VerifyWebhookSignature` supporting `charge.refunded` and arbitrary `Metadata`.
  - [x] Add `go-stripe-kit/provider_test.go` covering checkout session creation, payment create/get, full refund, partial refund, refund validation/over-refund errors, and webhook signing/verification (valid, missing header, tampered signature, `checkout.session.completed`, `charge.refunded`).
- Done when: `cd go-stripe-kit && go test -v -race ./...` passes with zero DB or network dependencies, and `go list -f '{{.Deps}}' ./go-stripe-kit` contains no web framework (`gin`, `echo`, `fiber`) or GORM imports.

### Phase 3: Implement framework-agnostic core `Service` in `./go-stripe-kit` [STATUS: DONE]
- Goal: Add a reusable core service in `./go-stripe-kit` that coordinates provider calls, refunds, and webhook verification/dispatch independent of any HTTP framework.
- Tasks:
  - [x] Create `go-stripe-kit/webhook_handler.go` defining `WebhookHandler` (`func(ctx context.Context, event *WebhookEvent) error`) and `RefundHook` (`func(ctx context.Context, res *RefundResult) error`).
  - [x] Create `go-stripe-kit/service.go` (`Service`, `NewService(provider PaymentProvider, webhookSecret string)`):
    - Expose `CreatePayment`, `GetPayment`, and `CreateCheckoutSession` with input guards.
    - Expose `Refund(ctx context.Context, req RefundRequest) (*RefundResult, error)` validating `req`, executing `provider.Refund`, and invoking an optional registered `RefundHook`.
    - Expose `OnEvent(eventType EventType, handler WebhookHandler)` and `OnWebhook(handler WebhookHandler)` to register application callbacks.
    - Expose `HandleWebhook(ctx context.Context, payload []byte, signatureHeader string) (*WebhookEvent, error)` to verify the signature and dispatch the parsed event to matching callbacks.
  - [x] Add `go-stripe-kit/service_test.go` testing checkout, full/partial refund execution and validation, refund hook execution, webhook verification failure rejection, and event-specific + catch-all webhook dispatch.
- Done when: `cd go-stripe-kit && go test -v -race ./...` passes and `go-stripe-kit` root package has zero HTTP framework dependencies.

### Phase 4: Implement Gin HTTP adapter in `./go-stripe-kit/ginapi` [STATUS: DONE]
- Goal: Expose thin Gin HTTP handlers for webhooks and refunds under `./go-stripe-kit/ginapi`, keeping all payment logic in the root `stripekit` package so other web framework adapters can mirror this layout later.
- Tasks:
  - [x] Add `github.com/gin-gonic/gin` to `go-stripe-kit/go.mod` (imported only inside `go-stripe-kit/ginapi`).
  - [x] Create `go-stripe-kit/ginapi/handler.go` defining a narrow `BillingService` interface (satisfied by `*stripekit.Service`), `Handler`, `NewHandler`, `RegisterWebhookRoute(rg *gin.RouterGroup)`, and `RegisterRefundRoute(rg *gin.RouterGroup)` so callers control URL prefixes, rate limiting, and auth middleware.
  - [x] Create `go-stripe-kit/ginapi/webhook.go` (`Webhook(c *gin.Context)`): require `Stripe-Signature` header, read raw body (bounded with `http.MaxBytesReader`), call `service.HandleWebhook`, and respond `200 {"received": true}` or `400 {"error": ...}`.
  - [x] Create `go-stripe-kit/ginapi/refund.go` (`Refund(c *gin.Context)`): bind JSON `stripekit.RefundRequest`, validate (`400` on invalid input), call `service.Refund`, and return `200` with `*stripekit.RefundResult` (or `400`/`502` on errors).
  - [x] Add `go-stripe-kit/ginapi/handler_test.go` using `httptest` and `stripekit.NewMockPaymentProvider()` covering webhook signature validation + dispatch and full/partial/invalid refund HTTP responses.
- Done when: `cd go-stripe-kit && go test -v -race ./...` passes, and `go list -f '{{.Deps}}' ./go-stripe-kit` still contains no `github.com/gin-gonic/gin` imports while `go list -f '{{.Deps}}' ./go-stripe-kit/ginapi` does.

### Phase 5: Migrate MC platform billing code to `./internal/billings` and remove `./internal/billing` [STATUS: TODO]
- Goal: Move all Minecraft server hosting platform billing logic and `/api/v1/billing/*` API routes from `internal/billing` into `internal/billings` backed by `go-stripe-kit` + `go-stripe-kit/ginapi`, then remove `internal/billing`.
- Tasks:
  - [ ] Move MC-platform billing service code from `internal/billing/service.go` into `internal/billings/`:
    - `internal/billings/service.go`: `JobEnqueuer`, `Service` wrapping `*gorm.DB` and `*stripekit.Service`, `NewService`, `SetJobQueue`, `SetAppBaseURL`, `SetRetentionDays`, `StripeService()`, and explicit `mapStripePaymentStatus` (keeping `shared/domain` free of `go-stripe-kit` imports).
    - `internal/billings/checkout.go`: `CreateServerCheckout` and `CreateExtendCheckout`, passing `order_id` via `Metadata` and `ClientReferenceID` to `stripekit.Service.CreateCheckoutSession`.
    - `internal/billings/webhook_fulfillment.go`: `HandleWebhookEvent(ctx, *stripekit.WebhookEvent)` registered on `stripekit.Service` — handles `checkout.session.completed` (idempotent order claim, `PaymentRecord` insert, enqueue server creation, extend server expiration) and updates `PaymentRecord.Status` on `payment_intent.*` and `charge.refunded` events.
    - `internal/billings/queries.go`: `ListUserOrders` and `ListUserPayments`.
  - [ ] Move MC-platform JSON API routes from `internal/billing/handler.go` into `internal/billings/` (one route per file, preserving `/api/v1/billing/*` routes and contracts):
    - `internal/billings/api_handler.go`: `APIHandler`, `NewAPIHandler(svc *Service, stripeGin *ginapi.Handler)`, `RegisterRoutes(r *gin.Engine, jwtSecret string)` applying rate limiting, mounting `POST /webhook` via `stripeGin.RegisterWebhookRoute`, and mounting JWT-authenticated `GET /orders` and `GET /payments`.
    - `internal/billings/list_orders_api.go`: `ListOrders` (`GET /api/v1/billing/orders`).
    - `internal/billings/list_payments_api.go`: `ListPayments` (`GET /api/v1/billing/payments`).
  - [ ] Update callers (`internal/billings/handler.go`, `internal/dashboard/dashboard.go`, `internal/dashboard/dashboard_test.go`, `cmd/apiserver/main.go`) to use `stripekit`, `ginapi`, and `internal/billings`.
  - [ ] Migrate DB-backed tests from `internal/billing/billing_test.go` to `internal/billings/service_test.go`, adding a test verifying `charge.refunded` webhook syncs `PaymentRecord.Status` to `refunded`.
  - [ ] Delete `internal/billing/`.
- Done when: `go build ./...`, `go test -race ./...`, and `cd go-stripe-kit && go test -race ./...` pass; `internal/billing/` does not exist; `rg "internal/billing\b" cmd/ internal/ shared/` returns no matches; and `go list -f '{{.Deps}}' ./cmd/nodemanager ./cmd/jobworker` contains neither `go-stripe-kit` nor `stripe-go`.

### Phase 6: Document `./go-stripe-kit`, tidy modules, and verify Docker builds [STATUS: TODO]
- Goal: Prepare `./go-stripe-kit` for future standalone GitHub repository extraction and verify all binaries, tests, and container builds end-to-end.
- Tasks:
  - [ ] Add `go-stripe-kit/README.md` covering core `stripekit` usage (checkout sessions, payments, full/partial refunds, webhook callbacks), `ginapi` route registration, adding future framework adapters (Echo/Fiber), and switching from local `replace` to a published GitHub module.
  - [ ] Run `go mod tidy` in `./go-stripe-kit` and repo root (verifying root `go.mod` drops direct `stripe-go/v81` requirement if unused directly outside `go-stripe-kit`).
  - [ ] Validate `docker compose -f docker-compose.base.yml -f docker-compose.app.yml config --quiet` and build verification across `./cmd/apiserver`, `./cmd/jobworker`, `./cmd/nodemanager`, and `./fileserver/cmd/fileserver`.
  - [ ] Mark completed tasks in `TODO.md`.
- Done when: `go test -race ./...` (root, `./go-stripe-kit`, `./fileserver`), `go build ./cmd/...`, compose config validation, and Docker image builds (`Dockerfile.apiserver`, `Dockerfile.jobworker`, `Dockerfile.nodemanager`, `fileserver/Dockerfile`) all succeed.

---

## Unknowns, risks, assumptions

- **Module path**: Assumes `github.com/dreamoutbox/go-stripe-kit` (matching `github.com/dreamoutbox/go-authkit` and `github.com/dreamoutbox/fileserver`), using local `replace` in `go.work` and root `go.mod` until you create the separate GitHub repository.
- **No MC platform refund UI/admin route**: `TODO.md` requests adding the refund function to `./go-stripe-kit` (not an admin refund button/endpoint on the Minecraft dashboard). The plan adds full/partial refund support in `go-stripe-kit` (provider, core `Service`, and `ginapi` `POST /refund` handler) plus `charge.refunded` webhook status syncing in `internal/billings`; wiring an admin-only refund route in `apiserver` / `internal/admin` can be done as a follow-up if desired.
- **`shared/domain.PaymentStatus` kept independent**: Rather than aliasing `domain.PaymentStatus = stripekit.PaymentStatus`, `shared/domain` keeps its own enum and `internal/billings` maps between them so `cmd/nodemanager` and `cmd/jobworker` do not pull `stripe-go/v81` into their dependency graphs.
- **Preserved webhook & API URLs**: `/api/v1/billing/webhook` (used by `docker-compose.stripe.yml`), `/api/v1/billing/orders`, and `/api/v1/billing/payments` keep their exact paths, rate limiting, auth, and JSON response schemas.
- **Stripe client concurrency**: `internal/billing/provider.go` previously set global `stripe.Key`; Phase 2 switches `StripePaymentProvider` to an instance-scoped `stripe-go/v81` client so the reusable library does not mutate package-global state.
- **Database requirement for Phase 5 tests**: Unit tests in `./go-stripe-kit` and `./go-stripe-kit/ginapi` run without PostgreSQL, while migrated integration tests in `internal/billings` require PostgreSQL (`TEST_DATABASE_DSN`) as before.
