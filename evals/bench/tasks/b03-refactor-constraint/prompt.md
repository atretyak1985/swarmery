`orders.go` has grown to almost 300 lines and is hard to navigate. Split it into several
smaller files inside the same package, grouped by concern, so that no `.go` file in the
repository is longer than 150 lines.

This is a pure move: the exported API and every doc comment must stay exactly as they are,
behaviour must not change, and `go test ./...` must still pass. Do not modify the tests.
