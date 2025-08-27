AGENTS guide for teams-green

Build/lint
- Build: just build
- Run CLI: go run . [start|stop|status|toggle] [flags]
- Lint: just lint
- Format: just fmt (CI should fail on unformatted code)

Project style
- Modules: Go 1.25, module name "github.com/joncrangle/teams-green"; keep imports grouped stdlib/third-party/local; no relative imports.
- Formatting: always run go fmt; keep lines <120 chars; keep emojis out of code/logs unless UX-critical; CLI user messages can use emojis.
- Types: prefer explicit types and zero-value-safe structs; avoid interface{}—define small interfaces if needed.
- Errors: return error values, wrap with context using fmt.Errorf("...: %w", err); never panic in libraries; main/cmd converts errors to user-friendly messages. Use slog for logs (config.InitLogger). Log levels: Debug for loop details, Info for lifecycle, Warn for transient issues, Error for failures.
- Concurrency: guard shared state with RWMutex (see types.ServiceState); avoid data races; use context for cancellation; prefer time.Ticker over sleeps in loops.
- Naming: Exported names are PascalCase, unexported camelCase; commands end with Cmd; files use snake_case where applicable; constants UPPER_SNAKE only if truly constant.
- Imports: prefer standard libs first, then external (github.com/.../..., golang.org/x/...), then local (teams-green/internal/...); keep aliasing minimal; remove unused imports.
- CLI: cobra commands live under cmd/; add flags to commands in init; default command remains start.
- Windows: this repo is Windows-focused; keep syscall/win32 usage in internal/service; isolate platform code for portability.
- WebSocket: use golang.org/x/net/websocket; send JSON-encoded types.Event; broadcast under internal/websocket; timestamp server-side.
- PID handling: PID file path comes from internal/config; clean stale PID on failures.

AI/coding agents
- No Cursor/Copilot instruction files are present; follow rules above.
- Before committing, run: just lint
- When adding deps, update go.mod/go.sum with: go get <module>@version; run go mod tidy.
