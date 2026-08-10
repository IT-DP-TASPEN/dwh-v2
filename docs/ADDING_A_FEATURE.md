# Adding a Feature

Features own their routes, handlers, business rules, persistence, permissions, navigation leaf, templates, and tests. Shared authentication, RBAC, rendering, and web plumbing stay under `internal/`.

This example adds a `customers` feature.

## 1. Add the schema

Create an operator-controlled Goose migration:

```sh
make migrate-create name=create_customers
```

Write reversible SQL and apply it with `make migrate`. Application startup never runs schema migrations.

## 2. Scaffold the feature

```sh
make feature name=customers
```

The command creates a compiling route, handler, permission definition, navigation leaf, and `web/templates/features/customers/index.html`. It refuses unsafe names and existing targets. It does not wire the feature or create speculative domain layers.

## 3. Add the domain model

Add `internal/features/customers/model.go` for records and view data actually used by the feature. Keep transport form fields separate from persisted records when their validation or security rules differ.

## 4. Parse and validate forms

Add `form.go` when the feature has mutations. Bound request bodies, normalize inputs once, return field-specific validation errors, and reject fields that belong to separate authoritative mutations.

## 5. Add SQL persistence

Add `repository.go` with parameterized SQL. Use transactions and row locks for invariants that must survive concurrency. If a mutation is audited, append the audit event through the same transaction before commit so either both changes persist or neither does.

## 6. Add business rules

Add `service.go` only when rules exist beyond request parsing and persistence. Accept `securityctx.Requester` for request-driven mutations. Authorization uses the effective identity. Convert its actor/effective snapshots to `audit.Attribution` at the mutation/audit boundary; do not re-read usernames after mutation.

## 7. Finish the handler

Render through `adminshell.Shell`, use themed `404`, `403`, conflict, and internal error responses, and preserve normal POST/redirect behavior. HTMX may enhance a flow but must not be required for correctness.

## 8. Register routes inside the feature

Keep paths and permission middleware in `routes.go`:

```go
func (handler *Handler) RegisterRoutes(router chi.Router) {
    router.With(handler.admin.RequirePermission(PermissionView)).Get("/customers", handler.Index)
}
```

All state changes must be POST-only.

## 9. Define permissions

Return immutable definitions from `PermissionDefinitions()`. Use stable `feature.action` keys, clear names, and a user-facing group. Permission sync is additive: removed or unknown database permissions are not automatically destroyed.

## 10. Define navigation

Return one `navigation.Item` from `Navigation()`. Its permission must be among the feature definitions. Containers and ordering belong to application composition.

## 11. Build templates

Keep feature pages under `web/templates/features/customers/`. A selected page automatically receives shared layouts/components/partials and sibling `_*.html` partials. Template output is escaped; do not introduce `template.HTML` for stored data.

## 12. Test beside the feature

Keep unit and handler tests in `internal/features/customers`. Put real-MySQL tests there with the `integration` build tag and use `internal/testutil/integrationdb`. Integration tests must require the explicit disposable `TEST_DB_*` configuration and never use `DB_NAME` as a fallback.

## 13. Wire one composition file

Edit only `internal/app/features.go` outside the feature directories:

1. append `customers.PermissionDefinitions()`;
2. place `customers.Navigation()` in the desired group;
3. construct dependencies and call `customers.NewHandler(...).RegisterRoutes(router)`.

No discovery, `init`, service container, or runtime registry is needed.

## 14. Synchronize and verify

Startup and the administrator CLI receive the same aggregate permission definitions, so the new key is synchronized on the next start. Then verify authorization, hidden navigation for permissionless users, active navigation state, direct-route denial, and admin bypass.

```sh
make fmt
make test
make verify
```
