AGENTS guide for teams-green

Build/lint/test
- Build: just build
- Test all: just test (or go test ./...)
- Test single package: go test ./internal/service (or go test -v ./internal/service for verbose)
- Test specific function: go test -run TestFunctionName ./path/to/package
- Run CLI: go run . [start|stop|status|toggle] [flags]
- Lint: just lint (golangci-lint run)
- Format: just fmt (golangci-lint fmt)
- Fix lint issues: just fix

Project style
- Go 1.25.0, module "github.com/joncrangle/teams-green"; imports: stdlib/external/local; no relative imports
- Formatting: always run go fmt; lines <120 chars; no emojis in code/logs (CLI messages ok)
- Types: explicit types, zero-value-safe structs; avoid interface{}—use small interfaces
- Errors: return errors, wrap with fmt.Errorf("context: %w", err); no panic in libs; use slog (config.InitLogger)
- Log levels: Debug (loop details), Info (lifecycle), Warn (transient), Error (failures)
- Concurrency: RWMutex for shared state; context for cancellation; time.Ticker over sleep loops
- Naming: PascalCase (exported), camelCase (unexported); commands end Cmd; files snake_case; constants UPPER_SNAKE
- CLI: cobra commands in cmd/; flags in init(); default command is start
- Windows-focused: syscall/win32 in internal/service; isolate platform code
- WebSocket: golang.org/x/net/websocket; JSON Events; broadcast in internal/websocket; server timestamps
- Testing: table-driven tests preferred; use testify/assert if needed; mock external dependencies

AI/coding agents
- No Cursor/Copilot files present; follow above rules
- Before commit: just lint && just test
- Add deps: go get module@version && go mod tidy
