# Docker deployment

Install Docker Engine with the Compose plugin. From the project directory,
create `archive-config` and copy your existing `config.yaml` into that directory
as `config.yaml` if the destination does not already exist. If you have no
existing config, copy `config.example.yaml` instead. For example, in PowerShell:

```powershell
New-Item -ItemType Directory -Force archive-config | Out-Null
if (!(Test-Path archive-config/config.yaml)) {
    if (Test-Path config.yaml) { Copy-Item config.yaml archive-config/config.yaml }
    else { Copy-Item config.example.yaml archive-config/config.yaml }
}
```

On a Linux server, use the equivalent commands from the project directory:

```sh
mkdir -p archive-config
if [ ! -e archive-config/config.yaml ]; then
  if [ -f config.yaml ]; then cp config.yaml archive-config/config.yaml
  else cp config.example.yaml archive-config/config.yaml; fi
fi
```

Edit the copied config to choose your targets. Keep `data_root: data` and
`token_env: GITHUB_TOKEN`: both services run in `/workspace`, and by default
the host `./data` directory is mounted at `/workspace/data`, preserving existing
local archive data in that directory. The directory containing
the config is mounted, so the panel can save edits atomically and the worker
can reload them. Do not add a token to the YAML.

For public targets no token is required. Optionally set `GITHUB_TOKEN` in the
shell that runs Compose for private targets and higher API limits. The
authenticated user feed is skipped when no token is available. Do not add a
token to the YAML or `.env` file.

To choose a different host data directory, create a `.env` file in the project
directory containing, for example:

```dotenv
ARCHIVE_DATA_DIR='C:/GitHub Archive/data'
```

Use an absolute path, or a path beginning with `./` relative to the project
directory. Both services mount this directory as `/workspace/data`; leave
`data_root: data` in the YAML. The launcher and ordinary Compose commands both
read `.env`. An `ARCHIVE_DATA_DIR` environment variable overrides this setting.
Changing the path selects a different archive; it does not move existing data.
To move an existing archive, stop both services, copy the entire old data
directory to the new location, and keep a backup until you verify the copy.

Then run:

Stop any existing host archive worker and panel first. Running two workers
against the same mirrors can race, and host and container processes should
not concurrently access the same SQLite archive through a shared mount.
When moving an existing archive to another server, stop the old worker and
panel before copying the complete `data` directory, including SQLite files and
all mirrors, to the project directory on the server. Keep a backup until you
have checked the copied archive.

```sh
docker compose up -d --build
```

Open http://127.0.0.1:8765 on that machine. When provided, Compose forwards the
token only to the archive process; it is visible to Docker administrators through normal
container inspection. `docker compose down` preserves the bind-mounted archive
directory. Both services need the same config directory and data directory.
Go image version can be overridden via
`GO_VERSION` if needed.

## Optional internet access

Use a server you control, with DNS for a domain pointing to it and inbound TCP
ports 80 and 443 reachable. The base configuration still binds the panel port
only to server loopback. The optional Caddy service supplies HTTPS and requires
authentication on every route. Caddy obtains and renews its certificate;
keep its named volumes. A hostname and credentials are required before the
remote override will start. Set these values in the deployment shell, not in
the repository:

```sh
export ARCHIVE_DOMAIN=archive.example.com
export ARCHIVE_USERNAME=your_username
# Enter a strong password at Caddy's prompt. Keep the resulting bcrypt hash private.
docker run --rm -it caddy:2-alpine caddy hash-password
read -r -p 'Paste the password hash: ' ARCHIVE_PASSWORD_HASH
export ARCHIVE_PASSWORD_HASH
# Optionally set GITHUB_TOKEN in this shell for private GitHub data.
docker compose -f compose.yaml -f compose.remote.yaml up -d --build
```

Visit `https://archive.example.com` after DNS and certificate issuance. The
hostname must be a plain DNS name without a scheme, port, or path, and must
match the browser address. The password hash and GitHub token are available to
Docker administrators via container configuration; restrict Docker access.
The browser sends the password to Caddy over HTTPS. HTTP is used only to
redirect to HTTPS and for ACME validation. Keep port 8765 bound to loopback;
do not publish it externally. Docker mode trusts the host and all containers
attached to `panel_net`, so do not attach untrusted containers to that network
or give them Docker access. The panel is not an authentication boundary: use
the authenticated Caddy proxy for every external request.
