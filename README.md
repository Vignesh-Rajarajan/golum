# Golum

A terminal coding agent, built in Go, in the spirit of Claude Code. Golum runs a
tool-using LLM in a loop against your codebase — reading and editing files,
running shell commands, searching, and remembering things across sessions —
inside a scrollable TUI, with every turn durably recorded so a session survives
a crash or a restart.

## What it does

| Capability | How it's supported |
|---|---|
| **Chat + tool-use loop** | `pkg/harness` drives a durable, resumable agent loop (`RunAgentLoop` → `Driver`) against an OpenAI-compatible streaming API (`pkg/llm`). Every step of the loop — model call, tool call, compaction — is decided by a pure reducer (`ReduceLaneState`) over an append-only record log, so the loop can be re-derived and resumed rather than re-run. |
| **Durable, resumable sessions** | Every entry (message, tool result, compaction) and every orchestration record (operation start/finish, step attempts, tool starts, queue events) is persisted to SQLite (`~/.golum/golum.db`, WAL mode) as it happens. If the process dies mid-turn, the next launch detects the suspended operation and offers `/resume-run` or `/abort-run` — no lost tool calls, no unbalanced context. |
| **Filesystem + shell tools** | `pkg/tool` implements `read_file`, `write_file`, `edit`, `list_dir`, `glob`, `grep`, `shell`, `todos`, and `memory`. All filesystem/shell access goes through `pkg/execenv`, which confines paths to the workspace, resolves symlinks, denies sensitive paths (`.env*`, `*.pem`, `.ssh/`, credentials), caps read size, and scrubs secrets from the shell environment. |
| **Approvals** | Mutating tools (`write_file`, `edit`, `shell`) require interactive `y`/`n` approval before they run; the decision can be pinned "always allow" for the rest of the session. |
| **Context compaction** | `pkg/contextmgr` + the harness `Compactor` tier context management: prune stale tool output first, then summarize older turns into a compaction entry once usage crosses ~80% of the context window — automatically, on overflow, or via `/compact`. Compaction is non-destructive: the full history stays in the log, so forking to a point before a compaction still sees the original turns. |
| **Session tree: branch, fork, resume** | Sessions are a tree, not a line. `/sessions` browses, resumes, or forks any past session at any entry; forking remaps compaction boundaries and orchestration state so the fork starts clean. |
| **Steering & follow-up queues** | Typing while the agent is mid-turn queues a steer message that's injected at the next model-step boundary, rather than being dropped or blocking; queued items are cancelable with `/cancel`. |
| **Long-term memory** | `pkg/memory` layers three tiers over SQLite + FTS5: procedural (`AGENTS.md` / project + user instructions), episodic (a digest of file changes and failures per turn), and semantic (a `go/ast`-derived map of the repo, rebuilt with `/reindex`). Surfaced to the model via the `memory` tool and `/memory`. |
| **Skills & prompt templates** | Drop a `SKILL.md`-style file under `.golum/skills/` to teach the agent a specialized procedure; drop a template under `.golum/commands/` to define a reusable `/name arg1 arg2` prompt with `$1`/`$ARGUMENTS` substitution. |
| **Hooks** | `pkg/hooks` exposes typed, mutation-capable extension points — `before_run`, `transform_context`, `before_request`, `before_tool`, `after_tool`, `before_compaction`, and more — for injecting behavior into the loop without touching it. |
| **Per-session model/tool config** | `/model`, `/think`, and `/tools` change the active model, thinking level, and tool set for the current session branch; the choice is recorded as an entry and replays consistently on reload or fork. |
| **Loop safety** | Guardrails on model invocations per turn, tool calls per turn, wall-clock time, consecutive tool errors, and repeated-tool-call loop detection (which injects a corrective system notice rather than failing silently). |

## Project structure

```
.
├── cmd/golum/              # Entry point: flags, session store wiring, Bubble Tea program
├── internal/ui/            # TUI (Bubble Tea): chat view, slash commands, session picker, approvals
├── pkg/
│   ├── config/              # Env-based configuration
│   ├── llm/                 # OpenAI-compatible streaming client
│   ├── harness/              # Agent loop: driver, reducer, compaction, hooks wiring, queues
│   │   └── session/          # Durable session tree: entries, records, SQLite store, fork/branch
│   ├── tool/                 # Tool implementations + registry + approval policy
│   ├── execenv/               # Sandboxed filesystem + shell used by tools
│   ├── contextmgr/            # Rolling context window + token accounting
│   ├── memory/                # Procedural / episodic / semantic memory over SQLite+FTS5
│   ├── skill/                  # SKILL.md loader
│   ├── prompt/                  # System prompt assembly + prompt templates
│   ├── hooks/                    # Typed lifecycle hooks
│   ├── sanitize/                  # Strips pseudo-tool markup from model text
│   ├── tokenizer/                  # Token counting/estimation
│   └── evals/                      # Behavioral eval harness (build tag: evals)
├── scripts/                 # Helper scripts (e.g. run-evals.sh)
└── examples/                # Standalone usage examples
```

## Setup

1. Copy `.env.example` to `.env`:
   ```bash
   cp .env.example .env
   ```

2. Add your API key:
   ```bash
   # OpenAI
   OPENAI_API_KEY=your_openai_api_key_here

   # OpenRouter (used instead of OpenAI if set)
   OPENROUTER_API_KEY=your_openrouter_api_key_here
   ```

3. Install dependencies and run:
   ```bash
   go mod download
   go run ./cmd/golum
   ```

### CLI flags

```bash
go run ./cmd/golum -list            # list saved sessions and exit
go run ./cmd/golum -resume <id>     # resume a saved session by id
go run ./cmd/golum -continue        # resume the most recently updated session
```

### Slash commands (in-app)

`/clear`, `/compact`, `/context`, `/sessions`, `/reindex`, `/memory [query|tier]`,
`/model <name>`, `/think <level>`, `/tools <name,...>`, `/cancel <queue-id>`,
`/resume-run`, `/abort-run`, `/help`, plus any custom templates you've dropped
under `.golum/commands/`.

## Configuration

| Variable | Purpose |
|---|---|
| `OPENAI_API_KEY` | OpenAI API key |
| `OPENROUTER_API_KEY` | OpenRouter API key (takes precedence over OpenAI) |
| `OPENAI_BASE_URL` | API base URL (default `https://api.openai.com/v1`) |
| `OPENAI_MODEL` | Model to use (default `gpt-4o`) |
| `GOLUM_CONTEXT_WINDOW` | Override the context window size used for compaction heuristics |
| `GOLUM_STREAM_TIMEOUT` | Per-request stream timeout, e.g. `10m`, `300s` |
| `GOLUM_LOG` | Set to enable file logging (see `pkg/applog`) |

Session data lives in `~/.golum/golum.db` (SQLite). Project-level agent
instructions go in `AGENTS.md`; user-level instructions in `~/.golum/AGENTS.md`.

## Dependencies

- [charm.land/bubbletea/v2](https://charm.land), [bubbles/v2](https://charm.land), [lipgloss/v2](https://charm.land), [glamour/v2](https://charm.land) — TUI
- [github.com/sashabaranov/go-openai](https://github.com/sashabaranov/go-openai) — OpenAI-compatible client
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure-Go SQLite (session + memory storage)
- [github.com/pkoukk/tiktoken-go](https://github.com/pkoukk/tiktoken-go) — token counting
- [github.com/google/uuid](https://github.com/google/uuid), [github.com/joho/godotenv](https://github.com/joho/godotenv)

## Documentation
- [Behavioral evals](./pkg/evals/README.md) — model-backed harness checks (`scripts/run-evals.sh`)
- [Examples](./examples/)

## License

MIT
