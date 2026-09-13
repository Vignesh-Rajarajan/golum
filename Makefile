.PHONY: test test-race test-pr test-merge bench evals

test:
	go test ./...

test-race:
	go test -race ./pkg/harness ./pkg/harness/session ./pkg/evals

test-pr: test
	go test ./pkg/harness ./pkg/harness/session ./pkg/evals ./pkg/evals/tasks/v1 ./pkg/evals/tasks/v2 ./pkg/tool ./pkg/execenv ./pkg/llm/...
	go test -race ./pkg/harness ./pkg/harness/session

test-merge: test-pr
	go test ./pkg/harness -count=1
	go test ./pkg/harness/session -count=1
	go test ./pkg/evals -count=1

bench:
	go test -bench=. -benchmem ./pkg/harness ./pkg/harness/session ./pkg/evals

evals:
	go test -tags evals ./pkg/evals/suite
