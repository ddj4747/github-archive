# Making and verifying changes

## Start in the right place

1. Read the root AGENTS.md and run `git status --short --branch`. This is a
   normal Git checkout with a private GitHub remote. Keep unrelated work.
   Archived bare mirrors are application data, not development checkouts.
2. Read the owning files and their tests before editing. Check the caller
   on both sides of a contract. For example, a list filter touches the
   form, query parameters, SQL ordering, paging, and the export path.
3. Develop against temporary config, SQLite files, and Git repositories.
   Treat `config.example.yaml` as the checked-in example. Never use a
   live mirror to test a force-push, deletion, or garbage collection.
4. Preserve compatibility with existing archive files and older rows.
   Changes to `internal/store/schema.go` should work on a blank database
   and on an existing archive. Do not clear cursors or rewrite archive
   times to make a new feature easier.

## Toolchain and commands

On the Windows development machine, Go may be absent from the default PATH.
Git must also be available.

```powershell
$env:PATH = 'C:\Program Files\Go\bin;' + $env:PATH
go version
git --version
go test -mod=readonly ./...
node --check internal/panel/web/app.js
```

Use `gofmt -w` on changed Go files. Run just the affected package while
iterating, for example `go test -mod=readonly -count=1 ./internal/panel`.
On a full verification pass, `go test -mod=readonly -count=1 ./...` avoids
using cached test results. Node is useful for syntax validation; the web
panel itself has no Node runtime or package build.

On this machine, restricted command execution can prevent Windows Git from
starting its local helper processes. Identify this from the actual failure,
then use the project's authorized execution route for the test. Do not
weaken tests or change Git security settings. The Docker build uses a Linux
Go image and compiles both binaries. The root dependency test expects the
Windows Go executable, so the image build alone does not run that test.

## Dependency lock

`modlock.go` and `modlock_test.go` are deliberate. The test checks the approved
direct requirements, the versions and checksums of the full module graph,
module cache provenance, and the absence of replacements. It runs the
specified Go binary with a controlled environment and workspace mode off.
Use `-mod=readonly`. A tidy check, when needed, is `go mod tidy -diff` so
that it does not rewrite the module. Do not weaken the lock or edit the
allowlist alongside an unapproved dependency change.

## Verification by change

| Change | Check through the real entry point |
| --- | --- |
| Config or target management | Load both example and edited config. Verify unknown fields fail, inheritance remains intact, and edits preserve unrelated YAML. Test paused and archive-only targets, case-insensitive search, and separate people/repository pagination. |
| GitHub client or collector | Use `httptest` against the package API base. Test cursor commit/rollback, pagination, errors, conditional requests, rate limits, and cancellation. Exercise repo and user polls with a real temporary SQLite database. |
| Ref or mirror changes | Use local remotes, unrelated Git tips, a deleted branch, and preserved objects. Verify full ref names, old/new SHAs, and that both commits remain readable after a later mirror update. |
| Dates, filters, or exports | Test mixed timestamps, missing timestamps, timezone bounds, and stable paging. Ref sources are observation, matching GitHub push, and tip commit date. Verify export order matches the list and filters are applied in SQL before paging. |
| History, source, or downloads | Open a retained commit from the panel. Check traversal and object ID rejection. Use Git to restore a test bundle in a clean checkout, and verify the ZIP contains the intended tracked files. |
| Browser interaction | Check the running panel after rebuilding its embedded assets. Verify keyboard access, date controls, search debounce, filtering, paging, links, and quiet refresh. Use synthetic data if the live archive cannot exercise a case. |

Keep URLs, filesystem paths, Git arguments, HTTP bodies, and result counts
bounded and validated. Use existing helpers such as `model.ValidSegment`,
`mirrorDirFor`, `targetCommit`, and `gitReader` where appropriate. Keep
history reads and downloads local, and never execute archived source files.
