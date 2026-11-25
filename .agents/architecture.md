# Architecture and code map

## Processes and data flow

1. `cmd/github-archive/main.go` loads and validates the config, reads the
   token from its named environment variable, opens SQLite, and starts
   `internal/poll.Run`. It notices valid config edits and reloads the schedule.
   It records a worker heartbeat and handles interrupt and SIGTERM.
2. `internal/poll/loop.go` schedules configured repositories, user feeds, and
   discovered repositories serially. The default interval is one minute
   (`model.DefaultPollInterval`); explicit overrides and GitHub's poll interval
   and backoff can change effective timing. Discovery persists its scan cursor
   and queue. The worker can miss a ref that moves and returns between polls.
3. Repository polls read the Events API and fetch Git refs. Git and event
   success can be saved independently if the other side fails. Only
   committed event inserts can advance the event cursor. Save the ref
   snapshot after the corresponding database transaction commits.
4. `cmd/github-archive-panel/main.go` starts a separate HTTP service against
   the same SQLite archive and mirror directories. It serves embedded assets
   from `internal/panel/web`. The browser refreshes the local archive roughly
   every 10 seconds; this is independent of the collector schedule.

## Ownership by package

| Area | Start here | What to keep intact |
| --- | --- | --- |
| Target model and config | `internal/model/{target,history}.go`, `internal/config/config.go`, `config.example.yaml` | Strict YAML keys, validated path segments, case-insensitive deduplication, explicit intervals, absolute resolved data root. `Load` requires the configured environment variable; `LoadForEditing` lets the panel validate settings without reading the secret. |
| GitHub requests | `internal/github/client.go`, `event_fetch.go`, `event.go`, `{repo,user,authenticated_user,discovery,search}.go` | HTTP and JSON parsing stay in this package. Pin the API origin, cap response bodies at 2 MiB and event pages at 10, stop at the saved cursor, and avoid logging secrets. An ETag is safe to reuse only for its persisted cursor. Honor GitHub polling and rate-limit headers without blocking unrelated targets. |
| Collector | `internal/poll/{loop,schedule,repo,user,authenticated_user,discovery}.go` | Cancellation, error and panic isolation, serial polling, bounded waits, transactional cursor advancement, and per-target logs. |
| Archive schema and store | `internal/store/{schema,store}.go` | DDL belongs in `schema.go`. The schema is additive and idempotent. Use WAL, a busy timeout, and one SQLite connection per handle. Call `sqlite.StrictPragmas(true)` immediately before opening each database handle. Use a plain filename or one SQL statement per `_pragma` parameter. |
| Git history | `internal/mirror/{remote,fetch,refs,recovery}.go` | Use system Git with explicit heads/tags refspecs and no pruning. Compare against the remote snapshot, since stale local refs are intentionally retained. Pin replaced/deleted old tips, check objects locally, and preserve Git config backups during conservative mirror recovery. |
| Panel and auth | `internal/panel/panel.go`, `cmd/github-archive-panel/main.go` | Register routes here. Keep local host, peer address, and write origin checks. Docker bridge mode is explicit. The trusted origin is an exact HTTPS origin behind Caddy. Target edits validate and replace the config atomically. |
| Lists, filters, exports | `internal/panel/{query,filters,targets,activity_dates,recent_refs,ref_tip_dates}.go` | Query the archived data and paginate. Use the same filters and sort for lists and exports. An actual observation, matching push, and tip commit time are distinct timestamp sources. |
| Historical state and downloads | `internal/panel/{state,history,history_changes,tree,commit_bundle}.go`, full repo bundle in `query.go` | Resolve exact saved commits, validate repository paths and object IDs, and read only retained local Git history. Bound views and stream downloads. |
| Panel search, logs, UI | `internal/panel/{search,logs}.go`, `internal/panel/web/{index.html,app.js,style.css}` | GitHub user search uses a public client, bounded cache, shared request/rate-limit handling, typing debounce, and GitHub-hosted avatars. Render untrusted data as text. Preserve focus and avoid replacing views with stale responses. |

## Archive contract

Paths below are relative to the resolved data root.

- `archive.sqlite` is the primary database. `activity_events` uses
  `(scope,event_id)`, so one event can appear once per feed scope.
  `ref_changes` uses `(owner,name,ref_name,old_sha,new_sha)`. Keep the five
  identity columns when adding metadata. `ref_observations` records new ref
  observation times and `ref_tip_dates` caches dates derived from local Git
  objects for historical rows. Derived times must never masquerade as actual
  observations.
- Repo event scope/feed: `repo:owner/name` / `repo-events:owner/name`. User
  event scope/feed: `user:login` / `user-events:login`. The authenticated
  user feed has a separate cursor. Ref feed: `refs:owner/name`. Preserve the
  existing keys so upgrades continue from saved cursors.
- `repos/<owner>/<name>/mirror.git` stores a bare Git repository.
  `repos/<owner>/<name>/ref-snapshot` represents the last saved remote
  heads and tags. `refs/keep/<sha>` pins old tips. The same repository
  directory holds `archive.log`; user logs live at
  `users/<login>/archive.log`. Logs rotate at 1 MiB with two old segments.
- Public user polls use the public events endpoint. The optional
  authenticated fetcher requires the token to authenticate as that user.
  Repository discovery watches repositories owned by a configured user;
  it does not turn contributions to arbitrary repositories into targets.
  Configured accessible private repos can have non-public repo events.

## UI and API entry points

- `/api/config`, `/api/targets`, `/api/stats`, and `/api/log` support
  management, filtering target lists, and worker diagnostics.
- `/api/events`, `/api/filter-options`, `/api/refs`, and
  `/api/recent-ref-changes` drive Activity and Ref History. Exports use
  `/api/export/events` and `/api/export/refs`. Dates entered in the browser
  are local calendar dates sent as an inclusive lower and exclusive upper
  instant. Undated refs are handled explicitly.
- `/api/event-state` and `/api/ref-state` resolve historical commits from
  archived records. A repository can be opened even when an event does not
  identify a commit. Avoid guessing that the current tip was the state at
  the time of an event.
- `/api/repository/{history,tree,file,snapshot,commit-bundle,bundle}`
  back the bookmarkable per-repo panel and downloads. A commit bundle
  contains the chosen commit and ancestors; a full bundle includes local
  refs, including preservation pins. ZIP snapshots include tracked files;
  Git LFS content and submodule repositories are outside their scope.
