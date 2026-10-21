# Contributing

If you want to contribute, here's how to do it without making things complicated.

## Bug reports and features
- Check the issues list first.
- If it's a bug, include a minimal way to reproduce it.
- If it's a feature, explain why it's needed and how it fits the core goal (handling high load safely).

## Rules for code
- Follow standard Go style (gofmt, goimports).
- All new stuff needs tests.
- Don't use global variables.
- Keep the public API clean.

## Pull requests

1. Fork the repo and make your branch.
2. Run the tests: `make test`. The recorder adapters are separate modules, so `go test ./...` at the root doesn't cover them.
3. Check for leaks: We use goleak in every test.
4. Lint your code: `make lint`.
5. Submit the PR.

## Testing checklist

- Unit tests: Must pass with `-race`.
- Performance: If you are changing the hot path, add or run benchmarks. 
- Coverage: Keep it high, but focus on meaningful tests rather than just hitting lines.

## Design principles

- Everything must be bounded. No infinite queues.
- Explicit backpressure is better than silent dropping.
- Metrics are not optional. If you add a feature, it probably needs a counter or a gauge.
- Safe shutdown is non-negotiable.

## Questions

Just open an issue if you're unsure about something.

## Modules and releases

The core module has no dependency on any metrics backend. `promrecorder`,
`otelrecorder` and `examples/observability` are separate modules with their own
`go.mod`; each uses a `replace` directive to build against the core in this
checkout. Dependencies are tidied per module: `cd <module> && go mod tidy`.

Sub-modules are tagged with their directory as a prefix. To release:

1. Tag the core, for example `v0.1.0`.
2. In each adapter, require that version and delete the `replace` directive:
   `go get github.com/iyashjayesh/go-adaptive-pool@v0.1.0`.
3. Tag the adapters: `promrecorder/v0.1.0`, `otelrecorder/v0.1.0`.

`examples/observability` is never imported, so it keeps its `replace`
directives and always builds against this checkout.
