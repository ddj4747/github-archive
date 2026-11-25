# GitHub Activity Archive

This repository contains a Go collector and a separate web panel. The collector
polls GitHub and records events and Git ref transitions locally. The panel
manages targets and browses, filters, and exports the local archive.

## Read first

- [.agents/architecture.md](.agents/architecture.md) maps the code and explains
  how data moves through the collector, mirrors, database, and panel.
- [.agents/development.md](.agents/development.md) explains how to make and
  verify changes, including the Windows toolchain and dependency lock.
- [.agents/operations.md](.agents/operations.md) covers the running Docker setup,
  updates, diagnosis, and preservation of live data.
- [README.md](README.md) is the user guide. [DOCKER.md](DOCKER.md) is the setup
  guide for local Docker and authenticated HTTPS deployments.

## Constraints to preserve

- Go module `githubarchive`, toolchain Go 1.27.4, standard library HTTP, and
  system Git. Keep `go.mod` and `go.sum` unchanged unless the user explicitly
  authorizes a dependency change. The module lock checks the full graph. The two
  direct dependencies are `gopkg.in/yaml.v3` and `modernc.org/sqlite`. Do not
  introduce go-github, go-git, or mattn/go-sqlite3.
- Use the official GitHub REST API and Git on github.com. Access stays within
  the configured token's permissions. The collector has no listener. It
  polls targets serially, bounds external work, and continues after failures.
- Keep archived rows and objects. Do not prune mirrors, garbage collect them
  from the service, or rewrite historical data to match the current remote.
  Keep old tips under `refs/keep/<sha>` and preserve empty SHAs for absent tips.
- Put secrets in the runtime environment. Keep credentials out of arguments,
  URLs, Git config, application logs, screenshots, exports, and commits.
  The panel operates without the collector's GitHub token.
- Protect live files. Under Docker the active config is
  `archive-config/config.yaml`, the archive is `data/`, and both services
  share them. The root `config.yaml` is for host runs. Keep local config, data,
  mirrors, logs, and caches out of Git. Use a separate temporary archive for
  experiments.
- Keep the panel bound to loopback by default. The optional internet setup
  uses the authenticated HTTPS reverse proxy documented in DOCKER.md. Keep
  host, origin, path, and download validation intact.

## Working conventions

- Start with the owner files in the architecture guide. Reuse the existing
  Go packages and embedded plain HTML, CSS, and JavaScript.
- Keep the change scoped. Preserve old data and existing API callers. When
  a filter changes, check the browser list, paging, live refresh, and export.
- Format Go edits with `gofmt`. For verification, use
  `go test -mod=readonly ./...` and `node --check internal/panel/web/app.js`
  as applicable. Use local Git fixtures and HTTP test servers.
- For a running Docker panel, rebuild before checking embedded browser assets.
  Recreate just `panel` for panel-only changes. See the operations guide
  before recreating `archive`, which needs its existing credential.
- Only publish changes or expose a service when the user authorizes that
  action. Review staged paths before pushing to the existing private GitHub
  remote.
