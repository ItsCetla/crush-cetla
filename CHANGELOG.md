# Changelog

All notable changes to this fork are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and versions follow
[Semantic Versioning](https://semver.org/). Upstream Crush changes are not
listed; see the [upstream releases](https://github.com/charmbracelet/crush/releases).

## [Unreleased]

## [0.2.0] - 2026-10-19

### Changed

- **Renamed to Ta-Da.** The binary is now `tada`, environment variables are
  `TADA_*`, config lives in `~/.config/tada/tada.json`, `tadarc`, `.tada/` and
  `TADA.md`. The repository moved to
  [ItsCetla/tada](https://github.com/ItsCetla/tada) and the site to
  [tada.cetla.dev](https://tada.cetla.dev).
- On first run, settings and data from `~/.config/crush` and
  `~/.local/share/crush` are copied into the new Ta-Da folders, and `CRUSH_*`
  environment variables are still honoured. Project `.crush/` session history
  is not moved; `.crush/skills` and `.crushignore` are still read.
- New "cetla" default theme, Ta-Da wordmark and a short startup intro
  (skip with any key; turn off with `TADA_NO_INTRO=1` or
  `options.tui.intro: false`).

### Added

- `tada update` (`--check`, `-y`): downloads the latest release from GitHub,
  checks it against `SHA256SUMS.txt` and replaces the binary.
- Background update check: at most every 6 hours, asks api.github.com for the
  latest release and tells you once per new version. Never downloads on its
  own. Turn it off with `options.update_check: false`,
  `option update-check false` or `TADA_NO_UPDATE_CHECK=1`.
- Windows installer (`tada_<version>_Windows_<arch>_Setup.exe`): per-user
  install without admin rights, adds `tada` to `PATH`, Start menu entry,
  uninstaller in Settings → Apps. Upgrading from the 0.1.0 Crush installer
  removes the old `crush.exe` and shortcuts.

## [0.1.0] - 2026-10-19

First tagged release of the fork, based on upstream Crush as of 2026-10-15.

### Added

- `crush trust`, `crush trust --list` and `crush trust --revoke`: a trust gate
  that keeps a cloned project's `crush.json`, `crushrc`, `.crush/` and
  OpenCode config inert until you say so. `CRUSH_TRUST_PROJECT=1` and
  `options.trusted_paths` are the non-interactive equivalents.
- OpenCode import: MCP servers, providers, model choices, the cached
  models.dev catalog, rules, skills, custom commands (`opencode:<name>`),
  tool toggles, deny permissions, `edit: allow` and `compaction.auto` are
  read from a local OpenCode install. `crush opencode` prints exactly what
  was imported and why anything was skipped. Toggle pieces in
  `options.opencode`; `CRUSH_DISABLE_OPENCODE=1` turns it all off.
- `permissions allow-in-project edit write …` (`permissions.project_allowed_tools`)
  lets file tools skip the prompt only inside the project directory.
- `option web-tools true` / `options.enable_web_tools` / `CRUSH_ENABLE_WEB_TOOLS`
  to opt in to the `fetch`, `agentic_fetch`, `download` and `sourcegraph` tools.
- The import report flags MCP servers that fetch packages on start (`npx`,
  `bunx`, `pnpm dlx`, `yarn dlx`, `@latest`).
- Windows, macOS and Linux release archives with `SHA256SUMS.txt`, built by
  the `release` workflow as a draft GitHub release.

### Changed

- The provider catalog is offline by default; refresh it only with
  `crush update-providers`.
- Bash auto-approval only applies to a single parsed read-only command.
  Wrappers (`env`, `timeout`, …), redirections, pipes and multi-line commands
  always ask. `env` dumps, `ps e` and `ls` outside the working directory ask too.
- Every web tool call and every `agentic_fetch` sub-agent request asks for
  permission first; `sourcegraph` asks before each search.
- `du` and `git blame` can no longer read files outside the project.
- Banned shell commands are also caught when hidden behind wrappers or paths.
- Config and lock directories are created readable only by the user, and the
  server's unix socket is private to its owner.
- Panic logs go to the log directory instead of the working directory.
- `crush --version` prefers the version passed at build time over the Go
  toolchain's VCS stamp.

### Removed

- PostHog telemetry and machine-id tracking.
- The startup update check.
- The Charm Hyper provider and the Catwalk/Hyper catalog fetches at startup.
- Automatic `.env` loading, the forced Termux DNS resolvers and the global
  pprof mux.
- Upstream release, CLA and labeler automation.

### Security

- Provider API keys are scrubbed from the agent's shell environment.
- Credential headers and key-like query parameters are redacted from HTTP
  debug logs; env values that fail to resolve are no longer logged.
- The `download` tool caps writes at 100 MiB and removes oversized files.
- The lint workflow no longer persists checkout credentials.

[Unreleased]: https://github.com/ItsCetla/crush-cetla/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/ItsCetla/crush-cetla/releases/tag/v0.1.0
