# Stash technical guide

How Stash works, how to run it in production, and how to develop it. For installing and using
Stash, see the [README](../README.md).

## Overview

Stash is a single Go program that uses only the standard library. It serves:

- **The owner's pages** (`/`), behind one password: files, folders, upload links and share links.
- **Upload link pages** (`/d/<token>`), where anyone with the link can add files to one folder,
  within that link's limits.
- **Share link pages** (`/s/<token>`), where anyone with the link can view and download a chosen set
  of files, or everything in one folder.
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
| `files/<id>` | The contents of each file, named by a random ID. Names, folders and dates are only in `stash.json`. Files in the trash stay here until they're deleted for good. |
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
| `-trash-days` | `STASH_TRASH_DAYS` | `30` | Days that deleted files and folders stay in the trash before they're deleted for good. From 1 to 3650. |
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

### Blocking password guessers with fail2ban

Stash logs every wrong password with the visitor's address, never the password itself:

```
2026/11/10 14:03:07 Wrong password from 203.0.113.9 for signing in.
2026/11/10 14:03:09 Wrong password from 198.51.100.4 for upload link "Tax papers". Further tries from there wait 1s.
```

Stash already slows each visitor down. [fail2ban](https://github.com/fail2ban/fail2ban) can go further
and block an address at the firewall. Set `STASH_TRUSTED_PROXIES` first: without it, the log shows
your reverse proxy's address for every visitor, and fail2ban would block the proxy.

The filter, in `/etc/fail2ban/filter.d/stash.conf`:

```ini
[Definition]
failregex = Wrong password from <HOST> for
```

The jail, in `/etc/fail2ban/jail.d/stash.local`. It reads Stash's log from the journal, where Podman
sends it on most Linux systems:

```ini
[stash]
enabled = true
filter = stash
# journalflags=1 also reads users' journals, where rootless containers log.
backend = systemd[journalflags=1]
journalmatch = CONTAINER_NAME=stash
port = http,https
maxretry = 10
findtime = 1h
bantime = 1d
```

`CONTAINER_NAME` is the container's name: `stash` in the README's commands and in the Quadlet file
above. Check that the lines are there with `journalctl --user CONTAINER_NAME=stash` (leave out
`--user` for a container that runs as root), then load the jail with `sudo fail2ban-client reload`.

Some limits to know:

- fail2ban blocks at the firewall of the machine it runs on. That works when visitors reach a
  reverse proxy on that machine, as above. A container port published straight to the internet may
  bypass the rules fail2ban adds.
- fail2ban blocks single addresses. Stash's own limit counts a whole IPv6 /64 as one visitor.

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
- Wrong passwords are counted per visitor and target. A link's page and the command line count
  towards the same limit.
- A visitor is one IPv4 address, or one IPv6 /64 network. An IPv6 connection comes with a whole /64,
  so taking a new address from it doesn't reset the count.
- After 5 failures, each further attempt has to wait twice as long as the one before, up to 15
  minutes.
- Nothing locks out everyone at once, so a stranger's guesses never keep the owner out. The flip
  side: someone with many addresses, such as a botnet or a larger IPv6 network, gets more guesses.
- Every wrong password is logged with the visitor's address and what it was for, so the owner can
  see guessing and block it with [fail2ban](#blocking-password-guessers-with-fail2ban). Attempts
  turned away during a wait aren't logged: they cost nothing, and would let anyone fill the log.

**Uploaded files.**
- Files are stored under random IDs. Names are cleaned: only the last path element, no control
  characters, at most 255 bytes. The folder path of an uploaded folder is cleaned the same way,
  element by element, and only names folders inside the upload's own folder.
- Only a short list of types opens in the browser: images, PDF, MP4/WebM/QuickTime video, and text.
  They're served with a fixed `Content-Type`, `X-Content-Type-Options: nosniff`, and a sandboxing
  `Content-Security-Policy`, except PDFs, which the browser's viewer can't show sandboxed. So a PDF
  received through an upload link is always a download, also in a folder that came with it.
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
- Files in the trash still take up disk space. They don't count against an upload link's limit, so
  deleting received files makes room on the link again.

## The trash

Deleting a file or folder moves it to the trash. Only the item the owner deleted gets a `trashed`
time in `stash.json`; what's inside a deleted folder is in the trash through that folder and comes
back with it.

- **Out of sight.** Items in the trash don't show in folders or search, can't be added to a new
  share link, and can't receive uploads. Share links leave out their files in the trash, on the page,
  in the zip and on the command line, until the owner restores them. For a shared folder, that's
  whatever is in the trash inside it, or everything while the folder itself is.
- **Upload links.** Deleting an upload link's folder revokes the link, so nobody uploads into the
  trash. Restoring the folder leaves the link revoked; the owner reopens it when they want to. A link
  can't be reopened while its folder is in the trash.
- **Restoring.** An item goes back to the folder it came from. If its name was used again meanwhile,
  it comes back as `name (2)`. Restoring something whose folder is in the trash too is refused:
  restore the folder first. Something deleted before its folder stays in the trash when the folder
  comes back.
- **Deleting for good.** Every 10 minutes, and when Stash starts, items deleted more than
  `-trash-days` ago are removed with everything in them. So are items the owner deletes from the
  trash, or everything when they empty it. Share links forget those files, and an upload link whose
  folder is gone is removed. An upload through that link that was still running is refused when it
  ends, rather than landing in My files.

## How uploads work

### From the browser: resumable chunks

1. `POST .../uploads` with `{"name", "size"}` checks the limits, sets the space aside, and returns an
   upload `id`. A file from an uploaded folder also sends `path`: the folder it was in, such as
   `Trip/Day 1` (see [Folders](#folders)).
2. `PUT .../uploads/<id>?offset=N` sends the bytes from position N, normally 32 MB at a time. The
   answer is `{"received"}`, or `{"done": true, "item"}` after the last byte.
3. If a chunk fails halfway, the page asks `GET .../uploads/<id>` how much arrived and continues
   from there. A `PUT` at the wrong offset gets `409` with the right position.
4. `DELETE .../uploads/<id>` cancels the upload and gives the space back.

For the owner the base path is `/api/uploads`, with `parent` (a folder ID) in the start request. For
upload links it's `/api/public/d/<token>/uploads`.

### Folders

A folder is uploaded one file at a time, each with its `path`. Strangers can't make folders, so the
server makes them, inside `parent` or the link's folder:

- **Cleaned element by element.** `/` and `\` both separate folders. Each element is cleaned like a
  file name, and elements without a usable name (empty, `.` or `..`) are left out. The elements are
  only names to look up or make, so a path can't lead out of the upload's folder. At most 64
  folders inside each other.
- **Made when the file is done**, not when the upload starts. An upload that fails or is cancelled
  leaves no empty folder, and a stranger can only make folders by sending files, within the link's
  limits.
- **Added to, not numbered.** A folder with the same name, ignoring case, gets the file, so
  uploading `Trip` twice fills one `Trip`, as does every file of one upload. Folders in the trash
  don't count: the file gets a new folder. Only when a *file* has the name does the folder become
  `name (2)`, or the first such name that's a folder or still free, so all files still land in the
  same one.
- **Counted on the link.** An upload link's size and file limits count the files in its folder and
  in every folder below it, leaving out what's in the trash.

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
| `PATCH /api/items/<id>` | `{name}`: rename a file or folder. A name already taken in that folder is refused. Renaming an upload link's folder renames the link too. |
| `POST /api/items/trash` | `{ids}`: move files and folders to the trash |
| `POST /api/items/restore` | `{ids}`: take them out of the trash again |
| `POST /api/items/delete` | `{ids}`: delete items in the trash for good. Items that aren't in the trash are left alone. |
| `POST /api/trash/empty` | Delete everything in the trash for good |
| `GET /api/files/<id>/content` | File contents. Add `?download` to force a download. |
| `POST /api/uploads`, `GET`/`PUT`/`DELETE /api/uploads/<id>` | Chunked upload |
| `POST /api/links`, `PATCH /api/links/<id>` | Create or change an upload link |
| `POST /api/shares`, `PATCH /api/shares/<id>` | Create or change a share link |

Link fields: `name`, `quota` (bytes), `maxFiles`, `expiresDays` (0 means never), `password` (an
empty string removes it), `message`, `revoked`. Share links use `maxDownloads` instead of `quota`
and `maxFiles`, and either `files` (IDs) or `folder` (one folder ID), only when creating.

A share link with `files` offers exactly those files. One with `folder` offers what's in that folder
at the moment someone looks, subfolders included, so files the owner adds later are shared too. "My
files" itself can't be shared, because it holds every upload link's folder.

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
| `GET /api/public/s/<token>` | Status and file list, or `locked`. In a shared folder, each file has a `path`: the subfolder it's in, such as `Day 1/Morning`. |
| `POST /api/public/s/<token>/unlock` | `{password}` |
| `GET /s/<token>/files/<id>` | One file. Add `?download` to download it instead of opening it. Counts as one download. |
| `GET /s/<token>/all.zip` | All files as one zip, stored uncompressed, with a shared folder's subfolders. Counts as one download. |

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
- Files and folders can't be moved to another folder yet.
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

The QR codes for links are made in the page by a small encoder in `ui.js`: byte mode, error
correction level M, versions 1 to 40, and the mask with the lowest penalty. No link goes to a QR
service. A link of about 50 characters becomes a version 4 code.

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

GitHub runs both for every push to `main` and every pull request (`.github/workflows/test.yml`).

Each test starts its own Stash with an empty data folder and uses it over HTTP, the way browsers,
curl and PowerShell do. `helpers_test.go` has the helpers: `newEnv` starts a Stash with the owner
signed in; `upload`, `put` (an upload like `curl -T`), `createLink` and `createShare` set things
up; `changeLink` and `changeShare` do what the pages can't, such as letting a link expire right
now. Passwords set that way use `fastHash`, which keeps the tests quick.

The tests don't cover the page scripts. After changing those, check by hand:

1. Start with an empty data folder, set the password with the printed link, and sign in.
2. Upload several files at once in the browser, including one larger than 32 MB.
3. Create an upload link with a small limit and a password, and upload through its page.
4. Create a share link, open it, and download one file and the zip. Share a folder with a
   subfolder, add a file to it, and check that the share page shows it under the subfolder's name.
5. Show a link's QR code, scan it with a phone, and download the image.
6. Delete a shared file and a folder from their menu. Check that the share page leaves the file
   out, restore both from the Trash, and empty the trash.
7. Look at the browser console for errors.

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
