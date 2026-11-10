# Stash technical guide

How Stash works, how to run it in production, and how to develop it. For installing and using
Stash, see the [README](../README.md).

## Overview

Stash is a single Go program that uses only the standard library. It serves:

- **The owner's pages** (`/`), behind one password: files, folders, upload links and share links.
- **Upload link pages** (`/d/<token>`), where anyone with the link can add files to one folder,
  within that link's limits.
- **Share link pages** (`/s/<token>`), where anyone with the link can view and download a chosen set
  of files.
- **A plain HTTP interface** for the command line: `PUT /d/<token>/<name>` uploads a file, and
  `GET` on a link without `Accept: text/html` prints instructions for a terminal.

The web pages are plain HTML, CSS and JavaScript without a build step. They are embedded in the
binary with `go:embed`, so the program is the whole application. The container image is the binary
on an empty (`scratch`) base, about 8 MB. Releases are on Docker Hub as `docker.io/dvdstelt/stash`,
for `linux/amd64`, `linux/arm64` and `linux/arm/v7` (see [Releasing](#releasing)).

## Data folder

Everything lives in one folder (`-data`, `/data` in the container):

| Path | Contents |
|---|---|
| `stash.json` | All metadata: files and folders, upload links, share links, the owner's password hash, and a random secret for signing cookies. |
| `files/<id>` | The contents of each file, named by a random ID. Names, folders and dates are only in `stash.json`. |
| `uploads/<id>.part` | Uploads in progress. Cleared when Stash starts, because an upload can't continue after a restart. |

`stash.json` is rewritten after every change: Stash writes a temporary file, syncs it to disk and
renames it over the old one, so a crash never leaves a half-written file. To back up, stop Stash and
copy the folder. Files and metadata are only consistent with each other when copied together.

## Settings

Every setting is a command-line flag or an environment variable.

| Flag | Environment variable | Default | Meaning |
|---|---|---|---|
| `-listen` | `STASH_LISTEN` | `:8080` | Address and port to listen on. |
| `-data` | `STASH_DATA` | `data` (`/data` in the container) | The data folder. In a container this is a path *inside* the container: mount your folder there instead of changing it. |
| `-public-url` | `STASH_PUBLIC_URL` | none | Base URL for links you hand out, such as `https://files.example.com`. Without it, Stash uses the address the owner's browser used. |
| `-trusted-proxies` | `STASH_TRUSTED_PROXIES` | none | Comma-separated IPs or CIDRs of reverse proxies whose `X-Forwarded-For`, `X-Forwarded-Proto` and `X-Forwarded-Host` headers Stash may believe. |
| `-ui-dir` | `STASH_UI_DIR` | built in | Serve the pages from this folder instead of the embedded copy. For development. |

Subcommand: `stash reset-password [-data DIR]` removes the owner password. Run it while Stash is
stopped; the next start prints a new setup link.

## Running in production

### HTTPS and a reverse proxy

Stash speaks plain HTTP. Put a reverse proxy in front of it for HTTPS, and set:

- `STASH_PUBLIC_URL` to the public address, so links you hand out are correct.
- `STASH_TRUSTED_PROXIES` to the address the proxy's connections come from, as Stash sees it.

What `STASH_TRUSTED_PROXIES` changes:

- **The visitor's address.** Stash takes it from `X-Forwarded-For`, used to slow down password
  guessing per visitor. When the setting is wrong or missing, every visitor appears to come from
  the proxy, so the limits apply to everyone together. That's stricter, not less safe.
- **HTTPS detection.** Session cookies only get the `Secure` flag when Stash knows the connection is
  HTTPS, either directly or from a trusted `X-Forwarded-Proto`.

Stash only believes `X-Forwarded-*` headers from addresses in this list, because anyone else can
send them. From the list of `X-Forwarded-For` addresses, it takes the right-most one that isn't a
trusted proxy.

**Caddy** gets a certificate automatically, streams request bodies, and has no size limit by default:

```
files.example.com {
	reverse_proxy 127.0.0.1:8080
}
```

**nginx** needs request buffering and the body size limit turned off for large uploads:

```nginx
server {
    listen 443 ssl;
    server_name files.example.com;
    # ssl_certificate and ssl_certificate_key go here

    client_max_body_size 0;
    proxy_request_buffering off;
    proxy_buffering off;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

**Cloudflare Tunnel and similar services** often limit the size of one request. Cloudflare's free
plan allows 100 MB. Browser uploads are sent in 32 MB chunks and still work. A command-line upload is
one request, so files above the limit need another route, such as direct access or a VPN like
Tailscale.

### Starting automatically with Podman

The README shows a minimal Quadlet file. With your own data folder and settings:

```ini
[Container]
Image=docker.io/dvdstelt/stash
ContainerName=stash
PublishPort=127.0.0.1:8080:8080
Volume=%h/stash-files:/data:Z
UserNS=keep-id
User=1000
Group=1000
EnvironmentFile=%h/.config/stash/stash.env

[Install]
WantedBy=default.target
```

Set `User` and `Group` to the output of `id -u` and `id -g`. Publishing on `127.0.0.1` keeps Stash
reachable only through the reverse proxy on the same machine.

## Security model

**The owner.**
- One password, hashed with PBKDF2-HMAC-SHA256 at 600,000 iterations.
- The first password can only be set with the setup link, which contains a random code that's
  printed to the log.
- Sign-ins are random 256-bit session tokens in an `HttpOnly`, `SameSite=Strict` cookie. Stash keeps
  only a SHA-256 of each token, in memory, for 30 days; a restart signs everyone out.
- Every changing request from the owner's pages also needs an `X-Stash: 1` header, which another
  site can't add without the browser asking Stash first.

**Links.**
- Tokens are 128 random bits.
- Link passwords are hashed like the owner's.
- A visitor who enters the right password gets a cookie containing an HMAC of the link and its
  password hash. Changing the password locks everyone out again.
- The command line sends the password in a `Link-Password` header on every request.
- Until the password is entered, a link reveals only its name and whether it expired.
- A share's download limit counts every request for a file's contents, and every zip. Resuming is
  free only for the browser that was counted: it gets a random ID in a `stash_downloader` cookie,
  and for 24 hours its `Range` requests for that file are free, also after the limit is reached.
  A `Range` header alone proves nothing, because anyone can send one.

**Guessing.**
- Wrong passwords are counted per visitor address and target.
- After 5 failures, each further attempt has to wait twice as long as the one before, up to 15
  minutes.

**Uploaded files.**
- Files are stored under random IDs. Names are cleaned: only the last path element, no control
  characters, at most 255 bytes.
- Only a short list of types opens in the browser: images, PDF, MP4/WebM/QuickTime video, and text.
  They're served with a fixed `Content-Type`, `X-Content-Type-Options: nosniff`, and a sandboxing
  `Content-Security-Policy`, except PDFs, which the browser's viewer can't show sandboxed.
- Everything else, including HTML and SVG, is served as a download (`application/octet-stream`,
  `Content-Disposition: attachment`).

**Pages.**
- A strict `Content-Security-Policy`: scripts only from Stash itself, no inline scripts, no framing.
- `Referrer-Policy: no-referrer`, because link tokens are in URLs.
- The pages build all HTML through a template tag that escapes every value.

**Space.**
- An upload sets its space aside before any data arrives, against the link's limit and the free disk
  space, minus a 512 MB reserve.
- Uploads in progress count against the limit, so parallel uploads can't overshoot it.
- A connection that sends nothing for 2 minutes is closed. Uploads themselves may take as long as
  they need.

## How uploads work

### From the browser: resumable chunks

1. `POST .../uploads` with `{"name", "size"}` checks the limits, sets the space aside, and returns an
   upload `id`.
2. `PUT .../uploads/<id>?offset=N` sends the bytes from position N, normally 32 MB at a time. The
   answer is `{"received"}`, or `{"done": true, "item"}` after the last byte.
3. If a chunk fails halfway, the page asks `GET .../uploads/<id>` how much arrived and continues
   from there. A `PUT` at the wrong offset gets `409` with the right position.
4. `DELETE .../uploads/<id>` cancels the upload and gives the space back.

For the owner the base path is `/api/uploads`, with `parent` (a folder ID) in the start request. For
upload links it's `/api/public/d/<token>/uploads`.

### From the command line: one request

`PUT /d/<token>/<name>` streams the whole body into a file. `curl -T file URL/` adds the file name to
a URL that ends in `/`. Replies are plain text, such as `Uploaded backup.zip, 12 GB.`, with
status 200. That's 200 rather than 201, because wget2 doesn't print the reply of a 201.

- **Without `Content-Length`** (`curl -T -` from a pipe), Stash sets aside all the space left on
  the link and settles on the real size at the end.
- **Refusing politely.** curl sends `Expect: 100-continue` for larger files and gets a refusal
  before sending anything. PowerShell and wget send the whole file first; for files up to 64 MB,
  Stash reads and discards the body before refusing, so they show the message instead of a broken
  connection.
- **Duplicate names.** A file with an existing name in the same folder is saved as `name (2).ext`,
  and the reply says so.

## HTTP interface

**Owner** (session cookie; changes also need `X-Stash: 1`):

| Method and path | Purpose |
|---|---|
| `GET /api/session` | `{signedIn, setupRequired}` |
| `POST /api/setup` | `{token, password}`: set the first password |
| `POST /api/login`, `POST /api/logout` | Sign in and out |
| `GET /api/state` | Everything: items, links, shares, storage, public origin |
| `POST /api/folders` | `{parent, name}` |
| `POST /api/items/delete` | `{ids}`: delete files |
| `GET /api/files/<id>/content` | File contents. Add `?download` to force a download. |
| `POST /api/uploads`, `GET`/`PUT`/`DELETE /api/uploads/<id>` | Chunked upload |
| `POST /api/links`, `PATCH /api/links/<id>` | Create or change an upload link |
| `POST /api/shares`, `PATCH /api/shares/<id>` | Create or change a share link |

Link fields: `name`, `quota` (bytes), `maxFiles`, `expiresDays` (0 means never), `password` (an
empty string removes it), `message`, `revoked`. Share links use `files` (IDs, only when creating)
and `maxDownloads` instead of `quota` and `maxFiles`.

**Upload links** (`<token>` from the link):

| Method and path | Purpose |
|---|---|
| `GET /d/<token>` | The upload page, or terminal instructions without `Accept: text/html` |
| `GET /api/public/d/<token>` | Status, limits and usage, or `locked` before the password |
| `POST /api/public/d/<token>/unlock` | `{password}`: sets the unlock cookie |
| `POST`, `GET`/`PUT`/`DELETE /api/public/d/<token>/uploads[/<id>]` | Chunked upload |
| `PUT /d/<token>/<name>` | Command-line upload. Password in `Link-Password`. |

**Share links:**

| Method and path | Purpose |
|---|---|
| `GET /s/<token>` | The share page, or download commands for a terminal |
| `GET /api/public/s/<token>` | Status and file list, or `locked` |
| `POST /api/public/s/<token>/unlock` | `{password}` |
| `GET /s/<token>/files/<id>` | One file. Add `?download` to download it instead of opening it. Counts as one download. |
| `GET /s/<token>/all.zip` | All files as one zip, stored uncompressed. Counts as one download. |

`HEAD` on a file or the zip doesn't count as a download, because it sends no contents. Checking a
link with `curl -I`, or a link scanner in a mail app doing so, doesn't use up a share's downloads.
Resuming doesn't count either, from the browser that started the download (see
[Security model](#security-model)).

## Limits and known gaps

- One owner. There are no user accounts.
- The owner's pages load all metadata at once, which is fine for tens of thousands of files.
- Thumbnails are the original images.
- Uploads can't continue after Stash restarts.
- Resuming a share download from the command line (`curl -C -` without a cookie jar) counts as a
  new download, and so does resuming after Stash restarts.
- Folders can't be renamed, moved or deleted yet. Deleting files works.
- The tests cover the server, not the page scripts. See [Checking changes](#checking-changes).

## Development

### Layout

| Path | Contents |
|---|---|
| `main.go` | Settings, startup, `reset-password` |
| `server.go` | Routes, pages, sign-in, serving file contents, housekeeping |
| `store.go` | Data model and `stash.json` |
| `auth.go` | Password hashing, sessions, unlock cookies, guessing limits, visitor address |
| `uploads.go` | Space reservations, chunked and command-line uploads |
| `api_owner.go`, `api_public.go` | The owner's interface, and the link and share pages |
| `disk_*.go` | Free disk space, per operating system |
| `*_test.go` | Tests, with the shared helpers in `helpers_test.go` |
| `ui/` | The pages: `index.html` (owner), `upload.html`, `share.html`, `login.html`, with `js/` and `css/` |

The page scripts are classic scripts, not modules, sharing a few globals: `StashUI` (`ui.js`:
templating, icons, formatting, shared components), `StashAPI` (`api.js`: requests and chunked
uploads) and `StashData` (`store.js`: the owner's data).

### Building and running

You don't need Go installed; the official Go image works:

```bash
podman run --rm -v "$PWD":/src:z -w /src -e CGO_ENABLED=0 docker.io/library/golang:1.27-alpine sh -c 'gofmt -l . ; go vet ./... && go build -o bin/stash .'
bin/stash -listen 127.0.0.1:8080 -data data -ui-dir ui
```

With `-ui-dir ui`, edits to the pages show on reload without rebuilding. The data folder and `bin/`
are ignored by git.

### Building the image yourself

```bash
podman build -t stash .
```

Then use `localhost/stash` wherever the README says `docker.io/dvdstelt/stash`. The build stage
runs on your own platform and cross-compiles, so `--platform linux/arm64` (or `linux/arm/v7`)
builds an image for another machine just as fast, without emulation.

### Checking changes

Run the checks and the tests:

```bash
podman run --rm -v "$PWD":/src:z -w /src -e CGO_ENABLED=0 docker.io/library/golang:1.27-alpine sh -c 'gofmt -l . ; go vet ./... && go test ./...'
```

`gofmt -l .` must print nothing. So much depends on `st.mu` that it's worth running the tests with
the race detector too. It needs a C compiler, which this installs in the throwaway container:

```bash
podman run --rm -v "$PWD":/src:z -w /src -e CGO_ENABLED=1 docker.io/library/golang:1.27-alpine sh -c 'apk add -q gcc musl-dev && go test -race ./...'
```

Each test starts its own Stash with an empty data folder and uses it over HTTP, the way browsers,
curl and PowerShell do. `helpers_test.go` has the helpers: `newEnv` starts a Stash with the owner
signed in; `upload`, `put` (an upload like `curl -T`), `createLink` and `createShare` set things
up; `changeLink` and `changeShare` do what the pages can't, such as letting a link expire right
now. Passwords set that way use `fastHash`, which keeps the tests quick.

The tests don't cover the page scripts. After changing those, check by hand:

1. Start with an empty data folder, set the password with the printed link, and sign in.
2. Upload several files at once in the browser, including one larger than 32 MB.
3. Create an upload link with a small limit and a password, and upload through its page.
4. Create a share link, open it, and download one file and the zip.
5. Look at the browser console for errors.

[AGENTS.md](../AGENTS.md) lists the conventions and the security rules changes must keep.

### Releasing

Pushing a version tag publishes the image to Docker Hub:

```bash
git tag -a v1.2.0 -m "Stash 1.2.0"
git push origin v1.2.0
```

The workflow in `.github/workflows/publish-image.yml` builds all three platforms and pushes
`docker.io/dvdstelt/stash` with the tags `1.2.0`, `1.2`, `1` and `latest`. A `0.x` version gets no
major-only tag, and a prerelease such as `v1.3.0-rc.1` gets only its own tag, never `latest`. People
who want to stay on one major version can run `docker.io/dvdstelt/stash:1`.

One-time setup: create a Docker Hub personal access token for the `dvdstelt` account with
**Read & Write** access, and store it as the repository secret `DOCKERHUB_TOKEN`
(`gh secret set DOCKERHUB_TOKEN`). The workflow pins each action to a commit; update those on
purpose, not automatically.
