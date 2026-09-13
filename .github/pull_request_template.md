## Summary

<!-- What changed and why. -->

## Test plan

- [ ] `gofmt` clean
- [ ] `go test ./...`
- [ ] `go test -race ./pkg/harness ./pkg/harness/session ./pkg/evals` if harness or evals changed
- [ ] CLI still builds (`go build ./cmd/golum`)
