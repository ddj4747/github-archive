# Local operation and deployment

## Know which instance you are working on

The normal local setup is Docker Compose. The base services are `archive`
(collector) and `panel` (web app). They share the image
`github-archive:local` and the host bind mounts `archive-config/` and
`data/`. The local panel is available at `http://127.0.0.1:8765/`.

| File or directory | Role |
| --- | --- |
| `config.example.yaml` | Versioned template. |
| `config.yaml` | Local settings for host runs such as StartArchive.ps1. |
| `archive-config/config.yaml` | Active Docker settings. The panel's target edits write this file and the worker reloads valid changes. |
| `data/` | Active Docker archive. Contains the SQLite database and sidecars, Git mirrors, snapshots, and logs. |
| `compose.yaml`, `Dockerfile` | Local build and runtime definition. |
| `compose.remote.yaml`, `Caddyfile` | Opt-in HTTPS and authentication on a separate server with a domain. Follow DOCKER.md. |

There should be one collector for an archive. Do not start a host worker
while Docker already owns that archive. Do not copy an active SQLite file
alone, delete history to clear a fault, or point a test instance at `data/`.

## Check before changing a running service

```powershell
docker compose ps
Invoke-RestMethod 'http://127.0.0.1:8765/api/stats'
```

A healthy panel and a recent worker heartbeat are separate signals.
`worker_running` confirms the worker checked in, while **Targets** shows the
last attempt and successful Git sync for each repository. **View log** shows
target-specific problems. The panel refresh control re-reads the local
archive. It does not send an immediate poll to GitHub.

For panel-only changes, rebuild the image and recreate just the panel:

```powershell
docker compose build panel
docker compose up -d --no-deps --no-build panel
```

For changes to the collector, follow DOCKER.md. Confirm that the existing
authorized GitHub credential is available as `GITHUB_TOKEN` in the shell
before recreating `archive`. The panel needs no token. Avoid running an
unqualified `docker compose up` after a rebuild when the token is missing,
since it can recreate the collector with an empty environment setting.
Never print full container environments or Git credential helper output.

## Common diagnostics

- **Docker CLI cannot connect:** Docker Desktop may still be starting, or the
  local command may lack access to the Docker engine. Check the engine
  before recreating anything and verify the services afterward.
- **Panel visible, events stale:** Check worker heartbeat, sync times, and the
  affected target log. Distinguish API delivery delay from Git transport
  faults. Recent Git ref changes can appear in Activity independently of a
  GitHub PushEvent. A rate-limit delay is expected to leave existing data
  intact.
- **Config edits do not take effect:** Check the active config path, validation,
  and the worker log. Terminal configuration and Docker configuration are
  separate.
- **Corrupt mirror config:** The fetch code can repair a confirmed malformed
  config when existing refs identify the object format. It keeps a
  `config.corrupt-*` copy and leaves objects and refs in place. Check the
  error and backup before any manual repair. Reinitializing or replacing a
  mirror can lose historical objects.
- **Older dates or missing historical state:** Older ref rows may have only
  the retained tip's commit date or no date. A tip date is not the time the
  ref changed. An event may have no exact commit, and any commit never
  captured by the mirror is unavailable for historical viewing.

## Move or publish safely

To move an archive, stop the services that use it and copy the entire data
directory, including SQLite sidecars and bare mirrors. Keep a backup until
the destination reads the expected archive. See DOCKER.md for the full
configuration and authenticated remote setup.

Keep the repository private unless the user explicitly changes that scope.
Git should contain source, templates, and docs only. Local config, archive
state, downloaded bundles, and credentials are excluded by .gitignore.
Review the actual staged paths before any upload. Do not use a shared
archive or expose port 8765 directly on the internet.
