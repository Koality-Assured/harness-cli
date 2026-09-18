# Repository AGENTS: harness-cli

Canonical rules for AI agents operating within `Koality-Assured/harness-cli`.

## Purpose & Boundaries

`harness-cli` is the dedicated standalone compiled binary repository implementing the unified Human-Agent CLI Control Plane. It provides zero-dependency cross-platform binaries across Windows, Linux, and macOS.

## Critical Directives

1. **Feature Parity**: Any modifications to CLI command flags, schemas, or behaviors MUST maintain strict parity with upstream specifications defined in `Koality-Assured/ai-harness-core` (Pillar 8) and `projects/harness-cli-methodology/` in `ai-router`.
2. **Sub-50ms Cold Startup**: Startup latency MUST remain under 50ms. Avoid heavyweight runtime initializations, large static assets, or synchronous network roundtrips during command initialization.
3. **Cross-Platform Compatibility**: Code MUST compile cleanly without CGO on `windows/amd64`, `windows/arm64`, `linux/amd64`, `linux/arm64`, `darwin/amd64`, and `darwin/arm64`.
4. **Security & Zero-Leak Credential Vault**: Secrets MUST never be written to plaintext config files, logs, or unmasked stdout. OS Keyring (DPAPI, Keychain, Secret Service) and AES-256-GCM encrypted fallback MUST enforce user-only private permissions (`0600` on POSIX, restricted ACLs on Windows).
5. **Conventional Commits & Branch Discipline**: All commit subjects MUST follow Conventional Commits (`feat:`, `fix:`, `refactor:`, `docs:`, `chore:`). Direct pushes to `main` are prohibited.
