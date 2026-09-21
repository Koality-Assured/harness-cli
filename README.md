# Harness CLI (`harness`)

Cross-platform compiled binary control plane for AI harnesses, domain routers, and autonomous agents.

[![Go Report Card](https://goreportcard.com/badge/github.com/Koality-Assured/harness-cli)](https://goreportcard.com/report/github.com/Koality-Assured/harness-cli)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

---

## Overview

`harness` is a zero-dependency, compiled native binary (Go) engineered for high-performance orchestration of AI agent harnesses (`ai-router`, `ai-harness-core`, and domain spokes). It replaces interpreter overhead with sub-50ms cold startups, guarantees mutual exclusion across concurrent agent worktrees, provides universal credential storage via native OS keyrings, and features an interactive terminal switcher (TUI).

### Key Features
- **Instantaneous Startup**: Cold startup latency under 15ms (benchmarked against Python's ~180-220ms).
- **Automated Worktree Isolation**: Enforces clean working trees, checks area claim locks, and manages git worktrees via `harness branch` and `harness clean`.
- **Dynamic Multi-Harness Switcher**: Interactive Bubble Tea TUI (`harness tui` or `harness`) to discover, inspect, and switch between registered domain harnesses.
- **Universal Credential Vault**: Zero-leak OS Keyring integration (Windows Credential Manager, macOS Keychain, Linux Secret Service) with AES-256-GCM encrypted fallback for headless environments.
- **Dual Output Modes**: ANSI terminal formatting for human operators, and `--json` for automation and agent parsing.
- **Multi-Platform**: Native binaries for Windows (`amd64`, `arm64`), macOS (`arm64`, `amd64`), and Linux (`amd64`, `arm64`).

---

## Installation

### Standalone Shell Script (macOS / Linux)
```bash
curl -fsSL https://raw.githubusercontent.com/Koality-Assured/harness-cli/main/install.sh | bash
```

### Standalone PowerShell (Windows)
```powershell
irm https://raw.githubusercontent.com/Koality-Assured/harness-cli/main/install.ps1 | iex
```

### Homebrew (macOS / Linux)
```bash
brew tap Koality-Assured/tap
brew install harness
```

### Scoop (Windows)
```powershell
scoop bucket add koality https://github.com/Koality-Assured/scoop-bucket.git
scoop install harness
```

### Building From Source
```bash
git clone https://github.com/Koality-Assured/harness-cli.git
cd harness-cli
go build -ldflags="-s -w" -o harness ./cmd/harness
```

---

## Command Reference

| Command | Description |
| :--- | :--- |
| `harness status` | Inspect git branch, working tree cleanliness, and active worktree claims. |
| `harness list` (or `ls`) | List all registered domain harnesses and live git states. |
| `harness switch <id>` | Switch active default harness in OS configuration. |
| `harness branch <slug>` | Create an isolated git worktree with Conventional branch and area claim lock. |
| `harness clean` | Prune merged worktrees and delete stale claims. |
| `harness agent [id]` | Inspect available agents and prompt specifications. |
| `harness auth login` | Authenticate with model providers (Anthropic, Cursor, Gemini, OpenAI). |
| `harness auth status` | Audit stored credentials, expiration times, and vault backend. |
| `harness auth logout` | Safely purge credentials from the OS vault. |
| `harness register <path>`| Register a local repository checkout as a domain harness. |
| `harness deregister <id>`| Remove a harness from the local catalog. |
| `harness scan [dir]` | Auto-discover sibling harness repositories matching router markers. |
| `harness tui` | Launch interactive terminal UI switcher. |

---

## Shell Ergonomics & Profile Integration

`harness` provides native shell autocompletion for subcommands, flags, and parameters across PowerShell, Bash, Zsh, and Fish.

### PowerShell Profile Integration
Add autocompletion loading to your PowerShell profile (`notepad $PROFILE`):

```powershell
if (Get-Command harness -ErrorAction SilentlyContinue) {
    harness completion powershell | Out-String | Invoke-Expression
}
```

### Bash Integration
```bash
# Add to ~/.bashrc
source <(harness completion bash)
```

### Zsh Integration
```zsh
# Add to ~/.zshrc
source <(harness completion zsh)
```

---

## License

MIT License. Copyright (c) 2026 Koality-Assured.
