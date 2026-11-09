# Stash

Stash is a place for your files that you run yourself, on your own computer or NAS. It can also
collect files from other people and send files to them, without anyone seeing more than you allow.

- **Your files.** Upload them, sort them into folders, and look at photos, PDFs, videos and text
  files right in your browser.
- **Upload links.** Need a file from someone? Send them an upload link. They can add files, but they
  can't see anything else. Every link has its own size limit: 25 MB for a few photos, 500 GB for a
  database backup. You can also limit the number of files, set an expiry date and add a password.
  A PDF received this way is downloaded rather than opened in the browser, whose PDF viewer runs
  outside the safeguards Stash puts on other previews.
- **Share links.** Want to send someone files? Pick them and send a share link. They can view and
  download only those files, one by one or all at once. Share links can expire, stop after a number
  of downloads (every file viewed or downloaded counts, so a limited share shows no previews), and
  have a password. A download that breaks off can be resumed in the same browser for a day,
  without counting again.

The people you send links to don't need an account. They upload from their browser, or, for very
large files, from the command line on Windows, macOS or Linux.

## What you need

- A computer or NAS that stays switched on.
- **Podman** or **Docker**, which run Stash in a container:
  - **Fedora** and similar systems usually have Podman already. Check with `podman --version`.
  - **Other Linux systems:** install `podman` or `docker` with your package manager.
  - **Windows and macOS:** install [Podman Desktop](https://podman-desktop.io) or
    [Docker Desktop](https://www.docker.com/products/docker-desktop/).
- Enough disk space for your files and for what people send you.

The commands below use `podman`. With Docker, type `docker` instead; everything else stays the same.

## Installing

**1. Start Stash.** The first time, this downloads Stash from Docker Hub:

```bash
podman run -d --name stash -p 8080:8080 -v stash-data:/data docker.io/dvdstelt/stash
```

Stash now runs on port 8080. It keeps everything in a storage volume called `stash-data`, so your
files survive restarts and updates. It runs on Intel and AMD computers and on ARM ones, such as a
Raspberry Pi.

**2. Choose your password.** Show Stash's log:

```bash
podman logs stash
```

It contains a line like this:

```
No password is set yet. Choose one here: http://localhost:8080/login?setup=…
```

Open that link in your browser and choose a password of at least 10 characters. From then on, open
http://localhost:8080 and sign in.

*Why a special link?* Until a password is set, whoever opens Stash first could choose one. The link
contains a code that only appears in Stash's log on your machine, so only you can use it. It works
once.

## Using Stash

### Your files

- Drag files onto the page, or click **Upload files**. Every file gets its own progress bar, and
  you can keep working while they upload.
- Click **New folder** to make a folder.
- Click a file to see it, download it, share it or delete it.
- Every file and folder has a **⋯** button with everything you can do with it. Right-clicking a
  file or folder shows the same menu.
- Use the search box at the top to find files. Pressing `/` jumps to it.

### Asking someone for files: upload links

1. Go to **Upload links** and click **New upload link**.
2. Give it a name, which they'll see as the title of the page, and choose a size limit. If you
   like, set an expiry date, a maximum number of files, a password and a message.
3. Copy the link and send it.

The person who opens the link sees your message, how much space is left, and an area to drag files
onto. They can't see files that are already there. If the link is full, has expired or was revoked,
they get a clear message instead.

Files arrive in **My files › Requests**, in a folder named after the link. You can change a link's
limits, revoke it or reopen it at any time.

### Sending someone files: share links

1. Tick the files you want to send and click **Create share link**. Or go to **Share links** and
   click **New share link**.
2. Give it a name and, if you like, an expiry date, a download limit, a password and a message.
3. Copy the link and send it.

They see the files with previews, and can download them one by one or all together as a zip file.

### Very large files: the command line

For huge files, a browser isn't always practical. Every upload link shows ready-made commands that
work in a terminal. They look like this:

| On | Command |
|---|---|
| macOS, Linux | `curl -T ./backup.zip https://files.example.com/d/LINK-CODE/` |
| Windows 10 and later | `curl.exe -T .\backup.zip https://files.example.com/d/LINK-CODE/` |
| PowerShell 7 | `Invoke-RestMethod -Method Put -InFile .\backup.zip -Uri https://files.example.com/d/LINK-CODE/backup.zip` |

- If the link has a password, add `-H "Link-Password: YOUR-PASSWORD"` to the curl command, or
  `-Headers @{ 'Link-Password' = 'YOUR-PASSWORD' }` to the PowerShell one.
- For very large files on Windows, use `curl.exe`. Older Windows PowerShell (5.1) loads the whole
  file into memory first.
- Share links work the same way: `curl -L -o files.zip https://files.example.com/s/LINK-CODE/all.zip`
  downloads everything.

## Keeping your files safe

### Where they are

In the `stash-data` volume. To see where that is on your disk:

```bash
podman volume inspect stash-data --format '{{.Mountpoint}}'
```

### Keeping them in a folder you choose (Linux)

A folder of your own is easier to back up. Start Stash like this instead of step 1:

```bash
mkdir -p ~/stash-files
podman run -d --name stash -p 8080:8080 --userns=keep-id --user "$(id -u):$(id -g)" -v ~/stash-files:/data:Z docker.io/dvdstelt/stash
```

With Docker, leave out `--userns=keep-id`. On Windows and macOS, stick with the volume.

### Backups

Everything is in that one volume or folder: your files, your links and your password. To back up,
stop Stash, copy the folder, and start it again:

```bash
podman stop stash
podman start stash
```

To save a volume as a single file instead: `podman volume export stash-data --output stash-backup.tar`.

## Updating

Download the newest version and remove the running container:

```bash
podman pull docker.io/dvdstelt/stash
podman rm -f stash
```

Then start it again with the same `podman run` command you used before. Your files and links stay
where they are.

If Stash starts automatically with Podman (see below), run `podman pull docker.io/dvdstelt/stash`
and then `systemctl --user restart stash` instead.

## Starting Stash automatically

**Docker:** add `--restart unless-stopped` to your `docker run` command.

**Podman:** let your system start it. Remove the container you started by hand
(`podman rm -f stash`), then create the file `~/.config/containers/systemd/stash.container`:

```ini
[Container]
Image=docker.io/dvdstelt/stash
ContainerName=stash
PublishPort=8080:8080
Volume=stash-data:/data

[Install]
WantedBy=default.target
```

Then start it:

```bash
systemctl --user daemon-reload
systemctl --user start stash
```

It now starts whenever you log in. To keep it running while you're logged out, also run
`loginctl enable-linger $USER`.

## Making your links work outside your home

At first, links only work on your own network. To let anyone on the internet use them, you need:

- a web address, such as `files.example.com`;
- a secure connection (HTTPS) in front of Stash, from a web server like Caddy or a tunnel service;
- and Stash told about that address, so the links it makes use it:
  `-e STASH_PUBLIC_URL=https://files.example.com` in the `podman run` command.

The [technical guide](docs/technical.md) shows how to set this up.

Once Stash is on the internet, strangers may try to guess your password. After 5 wrong tries, Stash
makes each next try wait longer. It also writes every wrong password to its log with the address it
came from, so `podman logs stash` shows you when it happens. The technical guide shows how to block
those addresses automatically with fail2ban.

## Forgot your password?

Stop Stash, remove the password, and start it again. The log then shows a new setup link:

```bash
podman stop stash
podman run --rm -v stash-data:/data docker.io/dvdstelt/stash reset-password
podman start stash
podman logs stash
```

If you keep your files in a folder, use the same `-v`, `--userns` and `--user` options as when you
started Stash.

## Settings

Stash works without any settings. These two matter once it's on the internet:

| Setting | What it does |
|---|---|
| `STASH_PUBLIC_URL` | The address people use, such as `https://files.example.com`. Links you hand out use it. |
| `STASH_TRUSTED_PROXIES` | The address of the web server in front of Stash, if there is one. |

Pass them with `-e NAME=value` in the `podman run` command, or put them in a file and use
`--env-file`. The [technical guide](docs/technical.md) lists all settings.

## For developers

See the [technical guide](docs/technical.md) for how Stash works, how to build the image yourself and
how releases reach Docker Hub, and
[AGENTS.md](AGENTS.md) for the rules coding agents follow in this project. Contributions are welcome.

## License

Stash is open source under the [Apache License 2.0](LICENSE). You may use, change and share it, also
commercially and in your own fork, as long as you keep the copyright notice and the
[NOTICE](NOTICE) file and mark the files you changed. Contributions are accepted under the same
license.
