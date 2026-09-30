# Contributing to Fragments Engine

Fragments Engine is pre-release software. Contributions are welcome; the bar is
correct, minimal, well-tested Go (and TypeScript for the Sysop UI).

## Before you start

- `README.md` has the quick start; `AGENTS.md` is the fastest orientation to the
  code layout and the boundaries that are not obvious from reading the code.
- `docs/architecture.md` explains the peer-application boundary and the
  ingest → classify → route → recall pipeline.
- License: see [`LICENSE`](LICENSE) and [`TRADEMARK.md`](TRADEMARK.md). Code
  contributions are accepted under the repository's MIT license; the Fragments
  Engine and Hollis Labs names remain protected marks.

## Workflow

1. **Open an issue or discussion first** for anything larger than a small fix.
2. **Branch from `main`** using `feat/<topic>`, `fix/<topic>` or `docs/<topic>`.
3. **Change one thing per branch** and keep commits small and coherent.
4. **Run the checks** before opening a pull request:

   ```bash
   make test    # go test ./...
   make lint    # go vet ./...
   ```

   A fresh clone needs `make sysop-build` once before `go build`, because the
   UI bundle is embedded but gitignored.
5. **Open a pull request.** Say what changed, what it deliberately leaves alone,
   and the commands you ran with their results. A maintainer will review it.

## Commit messages

Short imperative subjects; Conventional Commits (`feat(scope): …`, `fix: …`,
`docs: …`) are welcome. Use the body to explain *why*.

## Conventions

- The CLI, HTTP API and MCP are thin wrappers over `internal/service/` and must
  stay behavior-equivalent.
- Destination kinds are transport-oriented; never add a kind named after an app.
- Never commit `fragments.yaml`, databases, or credentials.

## Security

Report vulnerabilities privately as described in [`SECURITY.md`](SECURITY.md).
