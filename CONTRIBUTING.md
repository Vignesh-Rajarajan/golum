# Contributing

Merge automation lives in GitHub Actions, not local Git hooks. A pull request
is ready when the **CI** check is green.

## Development

```bash
cp .env.example .env   # then add a key if you want a live model
make test              # unit tests
make vet               # go vet
make test-race         # targeted race tests
make cover             # coverage.out
make build             # ./dist/golum
```

Model-backed evaluations are optional and never required for a merge:

```bash
scripts/run-evals.sh --model gpt-4o
```

See [pkg/evals/README.md](pkg/evals/README.md).

## Pull requests

Target `main`. Keep the change focused. Use the pull-request template. Do not
force-push to `main`.

## Releases

Maintainers publish by pushing a `vMAJOR.MINOR.PATCH` tag. The Release
workflow builds archives and attaches them to a GitHub Release.
