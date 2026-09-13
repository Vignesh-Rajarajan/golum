.PHONY: test test-race vet fmt-check cover build bench evals ci

test:
	go test ./...

test-race:
	go test -race ./pkg/harness ./pkg/harness/session ./pkg/evals

vet:
	go vet ./...

fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed on:" >&2; \
		echo "$$unformatted" >&2; \
		exit 1; \
	fi

cover:
	go test ./... -count=1 -coverprofile=coverage.out

build:
	mkdir -p dist
	CGO_ENABLED=0 go build -trimpath -o dist/golum ./cmd/golum

bench:
	go test -bench=. -benchmem ./pkg/harness ./pkg/harness/session ./pkg/evals

evals:
	scripts/run-evals.sh

ci: fmt-check vet test test-race build
