# AGENTS.md

Instructions for coding agents working on Stash. Read [docs/technical.md](docs/technical.md) first:
it explains the design, the data folder, the HTTP interface and the security model.

## The project in one paragraph

Stash is a self-hosted file store for one owner, with upload links (strangers add files within a
size limit, and see nothing else) and share links (strangers view and download chosen files). It's
one Go program using only the standard library, with plain HTML, CSS and JavaScript pages embedded
in the binary. People without a browser upload with curl, wget or PowerShell on Windows, macOS and
Linux. That's a core feature, not an extra.

## Build and check

Go doesn't have to be installed; use the official image:

```bash
podman run --rm -v "$PWD":/src:z -w /src -e CGO_ENABLED=0 docker.io/library/golang:1.27-alpine sh -c 'gofmt -l . ; go vet ./... && go build -o bin/stash .'
```

- `gofmt -l .` must print nothing, and `go vet ./...` must pass.
- Run with `bin/stash -listen 127.0.0.1:8080 -data data -ui-dir ui`. The log prints a setup link
  for the first password. `-ui-dir` serves the pages from disk, so page edits need only a reload.
- There are no automated tests yet. Check changes by hand, following "Checking changes" in
  `docs/technical.md`. Test command-line uploads with real `curl -T` commands, not only the pages.
- Build the container with `podman build -t stash .`. Never run `podman volume prune` or
  `podman system prune`: they delete the user's other volumes and images too.

## Rules that keep it simple

- **Go:** standard library only. Don't add modules to `go.mod`. If something seems to need a
  dependency, stop and ask.
- **Pages:** no framework, no bundler, no npm. Classic scripts loaded with `defer`, sharing
  `window.StashUI`, `window.StashAPI` and `window.StashData`.
- **No inline scripts or event handlers** (`onclick=`, `<script>…</script>` in HTML). The pages'
  Content-Security-Policy blocks them. Use `data-action` attributes, handled by `delegate()` in
  `ui.js`.
- **Build HTML only with the `html` template tag** from `ui.js`, which escapes every value. Never put
  a file name, link name, message or any other user text into `innerHTML` any other way.
- **Every change to metadata** happens with `st.mu` held and ends with `st.save()`.

## Security rules: don't break these

- **Never trust `X-Forwarded-For`, `X-Forwarded-Proto` or `X-Forwarded-Host` directly.** Use
  `srv.clientIP(r)`, `srv.isHTTPS(r)` and `srv.origin(r)`, which only believe proxies listed in
  `-trusted-proxies`. A trusted header is how the transfer.sh password-limit bypass happened.
- **Serve file contents only through `serveFile`.** It decides what may open in the browser (a short
  allowlist with a fixed `Content-Type` and a sandboxing CSP) and makes everything else, including
  HTML and SVG, a download. Don't add types to the inline list without that protection.
- **Every upload goes through `startUpload`.** It sets space aside against the link's limit and the
  free disk space before any byte is written. Never write a file that skipped that reservation.
- **Password-protected links reveal nothing before the password,** apart from their name and
  whether they expired. That holds for the pages and for command-line replies.
- **Compare tokens and hashes in constant time** (`tokenEqual`, `subtle`, `hmac.Equal`), and send
  password checks through the `limiter`.
- **Owner endpoints** go through `srv.owner(...)`, which needs the session cookie and, for
  changes, the `X-Stash: 1` header.

## Writing for people

Stash is meant for people who aren't developers, and for the strangers they send links to.

- Text in the pages and in command-line replies is plain and friendly, and tells people what to do
  next: "Too large: only 3 MB is left on this link."
- Command examples for link recipients cover macOS/Linux (`curl`), Windows (`curl.exe`), PowerShell
  7 and wget. Use `YOUR-PASSWORD` as the placeholder: PowerShell strips `<password>` from error
  messages.
- When behavior changes, update the [README](README.md) for users and `docs/technical.md` for
  developers in the same change.

## Commits

- Start the message with one category emoji: ✨ feature, 🐛 bug fix, 📝 docs, ⚙️ build or config,
  ⚜️ cleanup, 🚨 critical fix, ⚠️ breaking change.
- Say why the change was needed, not what the diff shows. Keep the first line under 72 characters.
- One logical change per commit. Don't add AI attribution lines such as `Co-Authored-By` or
  "Generated with".
