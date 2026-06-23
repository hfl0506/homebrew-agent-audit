# agent-audit

Local-first audit trails for AI coding agents.

`agent-audit` wraps command-line agents such as Codex, Claude, opencode, and Copilot. It records the session locally in SQLite: command argv, cwd, git metadata, PTY transcript, timing, exit code, and generic event records for richer adapters later.

It does not send logs to a cloud service.

## Install

For local development:

```sh
go install ./cmd/agent-audit
```

Homebrew support is scaffolded in `Formula/agent-audit.rb`. After the first GitHub release exists, publish a tap and install with:

```sh
brew tap hfl0506/agent-audit
brew install agent-audit
```

Release automation for the tap lives in `.goreleaser.yaml` and `.github/workflows/release.yml`. See `docs/homebrew.md`.

Versions are bumped automatically through Release Please. Commit messages like `feat: ...`, `fix: ...`, and `feat!: ...` determine the next semantic version and changelog entry.

## Usage

```sh
agent-audit run -- codex
agent-audit run --agent claude -- claude
agent-audit sessions
agent-audit show 1
agent-audit export 1
```

By default, the SQLite database lives under Go's user config directory:

```text
<user-config-dir>/agent-audit/audit.sqlite3
```

On macOS this usually resolves to:

```text
~/Library/Application Support/agent-audit/audit.sqlite3
```

Override it with:

```sh
AGENT_AUDIT_DB=/path/to/audit.sqlite3 agent-audit run -- codex
```

## Current Scope

The first implementation provides universal terminal capture through a PTY. It records what the wrapped agent prints and the top-level command process lifecycle. Structured internal tool calls depend on each agent exposing logs, hooks, or an adapter surface, and can be added incrementally.

## Development

```sh
go test ./...
go run ./cmd/agent-audit doctor
```
