# GitHub activity archive

GitHub Activity Archive saves GitHub events and Git history locally so you can
browse retained commits after a branch deletion or force-push.

- Track repositories and users, with optional discovery of repositories owned
  by the users you follow.
- Browse archived commits and files, compare ref changes, and download a ZIP
  snapshot or Git bundle.
- Filter and export events and ref changes as CSV or JSON.

## Getting started

The quickest setup uses Docker Compose. Follow the [Docker setup guide](DOCKER.md)
to configure your targets and open the panel at
[`http://127.0.0.1:8765`](http://127.0.0.1:8765). The guide also covers an
authenticated HTTPS deployment on your own server and domain.

On Windows, double-click `StartDocker.bat` to pull the latest changes from the
configured GitHub branch, start Docker Desktop if needed, build and start both
services, and see the panel address. The pull is fast-forward-only; if the local
branch has diverged or has conflicting edits, the launcher stops and reports the
problem. To launch a project
in another folder, set `ARCHIVE_PROJECT_DIR` to that folder before launching,
for example in PowerShell: `$env:ARCHIVE_PROJECT_DIR = 'C:\path\to\project'`.
You can also pass the folder as an argument: `StartDocker.bat "C:\path\to\project"`.
The argument takes precedence; if neither is set, it uses the folder containing
the batch file. It creates
`archive-config/config.yaml` only when it is missing. Configure your targets
in that file or in the panel. A token is optional: set `GITHUB_TOKEN` in the
launching environment if you need private access or higher API limits. See
DOCKER.md for choosing a different host directory for the archive data.

### Run without Docker

The collector polls the repositories and users in a YAML configuration. It stores
events and ref changes in `archive.sqlite`, with a bare Git repository and a
rotating `archive.log` for each configured repository. User logs are stored
under their own directories. User monitoring reads public events by default.
You can also enable separate repository discovery and authenticated event
fetchers for individual users.

Copy `config.example.yaml` to `config.yaml` and edit the target lists and data
directory. For private repositories or higher API limits, optionally set the
environment variable named by `token_env` to a GitHub token with the required
access. Do not put the token in the YAML file. For example, in PowerShell:

```powershell
$env:PATH = 'C:\Program Files\Go\bin;' + $env:PATH
# Optionally set GITHUB_TOKEN in your environment before starting.
go run -mod=readonly ./cmd/github-archive -config config.yaml
```

On Windows, `powershell -NoProfile -ExecutionPolicy Bypass -File .\StartArchive.ps1`
is an alternative. It uses the `GITHUB_TOKEN` environment variable when set,
or a GitHub credential already saved by Git Credential Manager; otherwise it
uses unauthenticated public access. To save credentials, sign in first with
`git credential-manager github login`. The script passes the credential to the
archiver in its process environment and does not write it to the configuration.
If your configuration uses a different token environment variable, pass
`-TokenEnv` with that variable's name.

Git must also be on PATH. Press Ctrl+C to stop the process. Relative data
paths resolve from the working directory. The default polling interval is one
minute, and individual targets can override it. The process polls one target
at a time and writes failures to that target's log. A large queue or GitHub
rate limits can extend the time between successful polls.
Each target poll has a 30-minute deadline so a stalled Git operation does not
block the remaining targets indefinitely.

To automatically archive repositories owned by a user, set
`discover_repositories: true` for that user:

```yaml
users:
  - login: octocat
    discover_repositories: true
    authenticated_events: true
```

Discovery scans the user's public repositories and, when a token is available,
filters the repositories visible to the token by owner. The latter can include private repositories the
token is permitted to list. It does not discover repositories owned by other
accounts where the user merely contributed. Each discovered repository is
queued for the same event and Git mirror polling as an explicitly configured
repository, using the user's poll interval. Discovery and its queue are saved
in SQLite, so scans and pending repositories continue after restart. Disabling
discovery stops automatic polling for that user but retains the existing
archive. An explicitly listed repository continues to be polled independently.
The token-visible listing is not available to every type of token; if it is
unavailable, the error is logged and public repository scans continue.

`authenticated_events: true` additionally polls GitHub's authenticated user
events endpoint with its own cursor. It can include private events only when
the token authenticates as that same user and has the required access (for
fine-grained tokens, Events user permission may be needed). Other users' events
remain public. Without a token, authenticated event polling is skipped. Both
settings are optional and default to false.

On the first poll, the mirror fetches the accessible heads and tags and the
events API supplies only its recent results, up to ten pages. Subsequent polls
continue from the saved cursors. Old rows and fetched Git objects remain after a
force-push or branch deletion; replaced and deleted tips are pinned under
`refs/keep/`. Changes that occur and disappear between polls, events outside
the API's available window, and data the token cannot access cannot be recovered.
The service polls GitHub's REST API and Git refs. Git ref changes appear
separately on Activity and are saved independently of the Events API. GitHub's
Events API can lag by 30 seconds to six hours and covers only supported event
types. Repository
listings can change while pages are being scanned; later scans retry discovery.
The archive can contain private repository data, so protect its directory and
back it up as appropriate.

## Development

On Windows, install Go 1.27.4 and put Git and Go on your PATH. Run
`go test -mod=readonly ./...`. The dependency lock test expects the standard
Go installation at `C:\Program Files\Go\bin\go.exe`. The tests use temporary
repositories and local HTTP test servers.

## Local management panel

Run the optional panel in a separate terminal, from the same working directory
as the archiver:

```powershell
$env:PATH = 'C:\Program Files\Go\bin;' + $env:PATH
go run -mod=readonly ./cmd/github-archive-panel -config config.yaml
```

Open `http://127.0.0.1:8765` in a browser. The panel listens only on a local
loopback address; keep it local because it provides access to the archive and
configuration. It does not need the token in its own environment. The running
archiver detects valid target changes and applies them automatically. Removing
a target or excluding a discovered repository stops its automatic polling and
retains the data already archived. Explicitly configured repositories are
polled independently of discovery exclusions.
The sidebar shows whether the archiver has recently checked in. Use “View log”
on a target to inspect errors when activity or mirrors are not appearing.

Use the panel to filter events and ref changes, export matching records as JSON
or CSV, and download a Git bundle for an existing mirror. CSV export prefixes
spreadsheet formula-like values for safety; JSON preserves exact values. A Git
bundle contains refs and the objects reachable from them. Ref history uses the
recorded observation time or a matching GitHub push. For older entries, the
panel can show the retained tip's commit date, which can predate the ref change.
Each row labels the source of its date.

In **Activity**, select an event type, user (the actor), or repository to filter
the recorded rows. The optional scope field accepts an exact feed scope such as
`repo:octocat/Hello-World` or `user:octocat`. The CSV and JSON exports use
the same filters. In **Targets**, type a GitHub username to see account
suggestions and avatars after a brief pause. Searches share a short cache and
pause when GitHub signals a rate limit. You can also enter a username manually.

Open an activity event to browse or download the commit recorded by a push
or completed pull request merge. Under **Ref history**, choose **Before** or
**After** to inspect a branch or tag change. For a deletion, **Before** opens
the last saved tip. A state is available when the record identifies a commit
that is retained in the mirror. Events such as stars do not identify one.

Choose **Browse files** to navigate folders and read text at the selected
commit. Larger text files and directory listings have paging controls.
A commit ZIP contains the tracked files at that commit. A commit bundle
contains the commit and its ancestors and can be opened with
`git clone snapshot-abc123.bundle restored` (use the downloaded filename).
Browsing and both downloads read from the local archive and use no GitHub API
quota.

The repository view also shows retained ref tips, including
`refs/keep/` pins for tips observed before a force-push or deletion. These
views can only show objects already fetched into the archive. Submodule contents
and Git LFS objects are separate and are not included in snapshots or bundles.

To restore a full downloaded Git bundle into a new working directory, use the
desired commit ID shown in the panel:

```sh
mkdir restored-repository
cd restored-repository
git init
git fetch ../archive.bundle 'refs/*:refs/archive/*'
git switch --detach COMMIT_ID
```

Replace `../archive.bundle` and `COMMIT_ID` with the actual bundle path and
commit ID. To restore just one snapshot, extract its ZIP and follow that
project's own installation instructions.

For Docker deployment, including an authenticated HTTPS setup for a server and
domain, see [DOCKER.md](DOCKER.md).
