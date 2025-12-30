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
2. Run the tests: `go test -race -v ./adaptivepool/`.
3. Check for leaks: We use goleak in every test.
4. Lint your code: `golangci-lint run ./...`.
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
