# Stash

A small self-hosted place for your files, with two kinds of links for other people:

- **Upload links** let someone send you files without seeing anything else. Each link has its own
  size limit (25 MB for a photo, 500 GB for a database), an optional file limit, expiry date and
  password.
- **Share links** let someone view and download files you picked, with an optional expiry date,
  download limit and password.

Uploads work from a browser (drag and drop, a progress bar per file, resumable chunks) and from the
command line on macOS, Linux and Windows, for files of any size.

Stash is one program with no dependencies. It keeps files on disk and its metadata in a single
JSON file, and is meant for one owner.

## Running it

With Podman or Docker:

```bash
podman build -t stash .
podman run -d --name stash -p 8080:8080 -v stash-data:/data stash
podman logs stash
```

The log shows a setup link like `http://localhost:8080/login?setup=…`. Open it to choose your
password. If you open Stash through another address, keep the `/login?setup=…` part.

Forgot the password? Stop Stash, clear it, and start again for a new setup link:

```bash
podman run --rm -v stash-data:/data stash reset-password
```

### Settings

Each setting is a command-line flag or an environment variable.

| Flag | Environment variable | Default | What it does |
|---|---|---|---|
| `-listen` | `STASH_LISTEN` | `:8080` | Address to listen on |
| `-data` | `STASH_DATA` | `data` | Folder for files and metadata |
| `-public-url` | `STASH_PUBLIC_URL` | the address you use | Base URL in the links you hand out, such as `https://files.example.com` |
| `-trusted-proxies` | `STASH_TRUSTED_PROXIES` | none | IPs or CIDRs of your reverse proxy, comma-separated |
| `-ui-dir` | `STASH_UI_DIR` | built in | Serve the web pages from a folder, for development |

### Behind a reverse proxy

Stash speaks plain HTTP; put it behind Caddy, nginx, Traefik or a Cloudflare tunnel for HTTPS.

- Set `-trusted-proxies` to the proxy's address. Only then does Stash believe the proxy's
  `X-Forwarded-For` and `X-Forwarded-Proto` headers. Anyone else can fake those headers, so
  without this setting Stash ignores them.
- Let large uploads through. For nginx: `client_max_body_size 0;` and `proxy_request_buffering off;`.
- Browsers upload in 32 MB chunks, so proxies with a request size limit (Cloudflare's free plan
  allows 100 MB) still work. Command-line uploads are one request, so they need a proxy without
  such a limit, or direct access.

## Uploading from the command line

Every upload link works with plain HTTP PUT. The file keeps its name.

| System | Command |
|---|---|
| macOS, Linux | `curl -T ./export.sql https://files.example.com/d/TOKEN/` |
| Windows 10 and later | `curl.exe -T .\export.sql https://files.example.com/d/TOKEN/` |
| PowerShell 7 | `Invoke-RestMethod -Method Put -InFile .\export.sql -Uri https://files.example.com/d/TOKEN/export.sql` |
| wget | `wget --method=PUT --body-file=./export.sql -O - https://files.example.com/d/TOKEN/export.sql` |

- If the link has a password, add it as a header: `-H "Link-Password: YOUR-PASSWORD"` for curl,
  or `-Headers @{ 'Link-Password' = 'YOUR-PASSWORD' }` for PowerShell.
- Several files at once with curl: `curl -T "{a.zip,b.zip}" https://files.example.com/d/TOKEN/`.
- Use curl for very large files. It streams the file, while Windows PowerShell 5.1 reads it into
  memory first.
- Opening a link with `curl https://files.example.com/d/TOKEN` prints these instructions.
  For a share link, it lists the download commands.

## Development

The program is Go with no external modules; the pages are plain HTML, CSS and JavaScript without a
build step, embedded into the binary.

```bash
podman run --rm -v "$PWD":/src:z -w /src -e CGO_ENABLED=0 docker.io/library/golang:1.27-alpine go build -o bin/stash .
bin/stash -listen 127.0.0.1:8080 -ui-dir ui
```

With `-ui-dir ui`, changes to the pages show up on reload without rebuilding.
