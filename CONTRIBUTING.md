# Contributing to TraceGen

Thanks for your interest in TraceGen — a single-binary OpenTelemetry trace generator. Bug reports, feature ideas, code, and docs are all welcome.

## Ways to contribute

- **Report a bug** or **request a feature** — open an issue with one of the [issue templates](.github/ISSUE_TEMPLATE).
- **Add your deployment** — running TraceGen somewhere? Add it to [`WHERE-TRACEGEN-RUNS.md`](WHERE-TRACEGEN-RUNS.md) via a pull request, or use the "Add a deployment" issue template and we'll add it for you.
- **Send a pull request** — see below.

## Building from source

TraceGen is a single Go module with no runtime dependencies.

```bash
git clone https://github.com/ImmersiveFusion/if-opentelemetry-tracegen.git
cd if-opentelemetry-tracegen
go build -o tracegen ./cmd/tracegen
./tracegen -insecure   # send to a local collector on localhost:4317
```

Cross-compile for another platform:

```bash
GOOS=linux   GOARCH=arm64 go build -o tracegen     ./cmd/tracegen
GOOS=darwin  GOARCH=arm64 go build -o tracegen     ./cmd/tracegen
GOOS=windows GOARCH=amd64 go build -o tracegen.exe ./cmd/tracegen
```

## Pull requests

1. Fork the repo and branch from `main`.
2. Keep changes focused — one logical change per PR.
3. Run `go build ./...` and `go vet ./...` before pushing.
4. Use clear, conventional commit messages (`feat:`, `fix:`, `docs:`, …).
5. Open the PR against `main` and describe what changed and why.

## Reporting issues

Use the issue templates. For bugs, include your platform/architecture, the exact `tracegen` command and flags, and what you expected versus what happened. `-log-level debug` gives more detail.

## Code of conduct

Be decent. We follow the spirit of the [Contributor Covenant](https://www.contributor-covenant.org/): no harassment, assume good faith, keep it about the work.

## License

By contributing, you agree your contributions are licensed under the repository's [Apache-2.0 license](LICENSE).
