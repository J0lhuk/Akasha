# Akasha

Use relevant installed skills from `~/.Codex/commands/` when available and applicable.
Read `docs/ARCHITECTURE.md` and the active step in `docs/IMPLEMENTATION_PLAN.md` before changing architecture.

- Product implementation is Go; embedded UI uses plain JavaScript/CSS. Python scripts collect public research and run independent end-to-end fixtures.
- Keep one SQLite database per project. Group boundaries apply before retrieval and caching. Never implicitly pool work and home memory.
- Preserve immutable revisions and both sides of conflicts. Secrets belong only in the encrypted vault; no secrets in research, docs, tests, logs or source control.
- Use generated fixture credentials for tests. Do not change global Codex configuration, hook trust or unrelated Docker services.
- Validate relevant changes with `go test ./...` and `go vet ./...`. The Go toolchain is 1.26.4. Quote PowerShell flag arguments that contain paths, e.g. `'-coverprofile=reports/coverage.out'`.
- `go run ./cmd/akasha-eval` runs the published 1000-record fixture. Do not change its questions to hide regressions; retain failed results and explain engine changes.
- `scripts/smoke.py` exercises the real CLI/MCP/hooks; `scripts/docker-smoke.py` creates and removes only its own isolated test container.
- Document actual measurements and limitations. Local context cache hits are not provider token-cache savings. Tests on a fixture do not prove coding quality on arbitrary repositories.
