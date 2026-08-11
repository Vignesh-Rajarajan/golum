#!/usr/bin/env bash
# Usage: scripts/run-evals.sh [--model NAME] [go test args...]
# Validates model selection + API key, creates a timestamped artifact dir,
# then runs: go test -tags evals ./pkg/evals/... -v "$@"
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

# Load .env when present so local keys work without exporting by hand.
if [[ -f .env ]]; then
  set -a
  # shellcheck disable=SC1091
  source .env
  set +a
fi

model_flag=""
pass=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --model)
      if [[ $# -lt 2 ]]; then
        echo "scripts/run-evals.sh: --model requires a value" >&2
        exit 2
      fi
      model_flag="$2"
      shift 2
      ;;
    --model=*)
      model_flag="${1#--model=}"
      shift
      ;;
    *)
      pass+=("$1")
      shift
      ;;
  esac
done

if [[ -n "$model_flag" ]]; then
  export GOLUM_EVAL_MODEL="$model_flag"
fi

if [[ -z "${GOLUM_EVAL_MODEL:-}" && -z "${OPENAI_MODEL:-}" ]]; then
  echo "scripts/run-evals.sh: set GOLUM_EVAL_MODEL or OPENAI_MODEL (or pass --model NAME)" >&2
  exit 2
fi

if [[ -z "${OPENAI_API_KEY:-}" && -z "${OPENROUTER_API_KEY:-}" ]]; then
  echo "scripts/run-evals.sh: set OPENAI_API_KEY or OPENROUTER_API_KEY" >&2
  exit 2
fi

ts="$(date -u +%Y%m%dT%H%M%SZ)"
# Prefer uuidgen; fall back to a random hex from /dev/urandom.
if command -v uuidgen >/dev/null 2>&1; then
  id="$(uuidgen | tr '[:upper:]' '[:lower:]')"
else
  id="$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
fi
artifact_dir="$root/pkg/evals/.eval/${ts}_${id}"
mkdir -p "$artifact_dir"
export GOLUM_EVAL_ARTIFACT_DIR="$artifact_dir"

echo "eval artifacts: $artifact_dir"
echo "model: ${GOLUM_EVAL_MODEL:-${OPENAI_MODEL}}"

exec go test -tags evals ./pkg/evals/... -v "${pass[@]+"${pass[@]}"}"
