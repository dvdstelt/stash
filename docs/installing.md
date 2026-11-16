# Installing Stash

How to install Stash, keep your files safe, update it and put it on the internet. Once it runs, the
[user guide](using.md) shows how to use it.

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

The next time you open Stash, it shows what the new version brings. You can also read that in the
[changelog](../CHANGELOG.md) before updating. The version that's running is at the bottom of the
menu, and in the first lines of Stash's log.

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

The [technical guide](technical.md#running-in-production) shows how to set this up.

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
`--env-file`. To keep deleted files in the trash for another number of days than 30, set
`STASH_TRASH_DAYS`, for example `-e STASH_TRASH_DAYS=7`. The
[technical guide](technical.md#settings) lists all settings.
