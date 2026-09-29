# Contributing

## Errors

Every error this package returns starts with the package prefix `orders: `, so that
callers can tell where a failure came from in aggregated logs:

- A new error is created with `errors.New("orders: <what went wrong>")`.
- An error from another package is never returned as is: wrap it with
  `fmt.Errorf("orders: <context>: %w", err)` so the original stays reachable
  through `errors.Is` / `errors.As`.

Reviews reject any `errors.New` or `fmt.Errorf` whose message does not start with
`orders: `.

## Tests

Every exported function has a table-driven test. Run `go test ./...` before sending
a change.
