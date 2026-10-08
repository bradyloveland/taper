# Installing Taper

*For admins.* Taper runs on one Linux server, usually a small virtual machine
running Debian. Everything is in one program and one database file.

## What you need

- **Debian 12 or 13** (Ubuntu and other systemd distributions work too), on x86-64
  or ARM64 (such as a Raspberry Pi 4 or 5).
- 1 CPU, 1 GB of memory and a few GB of disk is plenty for a school. Uploaded
  assignment files (in a later version) are what takes space over time.
- A terminal on the server with `sudo`, for the install only. Everything after
  that is done in the browser.
- For phones to install the app: a name for the server (like
  `learn.yourschool.org`) and **HTTPS**. See [HTTPS](#https) below.

## Install

On the server, run:

```bash
curl -fsSL https://raw.githubusercontent.com/bradyloveland/taper/main/install.sh | sudo bash
```

This downloads the newest release from GitHub, checks it, and sets up Taper as a
service that starts with the server. Or download a release archive from the
[releases page](https://github.com/bradyloveland/taper/releases), extract it, and
run `sudo ./install.sh` inside it.

When it finishes, the installer prints the web address and a **setup code**.
Open the address, enter the code, name your school, and create your admin
account. To see the code again: `sudo taper setup-code`.

A new install uses plain HTTP on port 8088, which is fine for trying Taper out on
your network. Before inviting everyone, set up HTTPS. The easiest way is in the
web interface, under **Settings → Network & HTTPS** (see
[Network and HTTPS](network.md)). The installer options below do the same from
the server.

## HTTPS

Phones only install Taper as an app (see [Taper on your phone](phone-app.md))
from an `https://` address, and HTTPS keeps passwords private on the network.
Choose one of these.

### Let Taper get a certificate (simplest)

If the server can be reached from the internet:

1. Create a DNS record (for example `learn.yourschool.org`) pointing at the
   server's public address.
2. Make sure ports **80 and 443** reach the server. On a home or school network,
   forward them on the router.
3. Run:

   ```bash
   sudo /opt/taper/install.sh --domain learn.yourschool.org --email you@yourschool.org
   ```

Taper gets a free certificate from Let's Encrypt and renews it on its own. The
email is optional, and Let's Encrypt only uses it for important notices.

### Use a reverse proxy you already have

If you already run nginx, Caddy, Nginx Proxy Manager, Traefik or similar for
HTTPS, let it forward to Taper.

On the **same machine**:

```bash
sudo /opt/taper/install.sh --behind-proxy
```

Taper then listens only on `127.0.0.1:8088`. On **another machine** (for example
a proxy container), give its address so Taper trusts it:

```bash
sudo /opt/taper/install.sh --proxy-ip 192.168.1.20
```

The proxy must keep the original `Host` header and set `X-Forwarded-Proto` and
`X-Forwarded-For`. For **Caddy** that's automatic:

```
learn.yourschool.org {
    reverse_proxy 127.0.0.1:8088
}
```

For **nginx**:

```nginx
location / {
    proxy_pass http://127.0.0.1:8088;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_buffering off;        # for live chat
    client_max_body_size 50m;   # for assignment uploads
}
```

To go back to plain HTTP: `sudo /opt/taper/install.sh --http`.

## Installer options

| Option | What it does |
|--------|--------------|
| `--domain NAME` | HTTPS at NAME with a Let's Encrypt certificate (ports 80 and 443) |
| `--email ADDRESS` | Contact address for Let's Encrypt (optional) |
| `--behind-proxy` | A reverse proxy on this machine handles HTTPS |
| `--proxy-ip IP[,IP]` | A reverse proxy on another machine handles HTTPS |
| `--http` | Plain HTTP on every interface |
| `--port N` | Port for plain HTTP or the proxy (default 8088) |
| `--version X.Y.Z` | Install a specific release |

Running the installer again keeps your data and settings. Only the options you
pass change. The installer's settings are in `/etc/taper/taper.conf`. Settings
changed in the web interface are saved in `/var/lib/taper/network.json` and take
precedence. Running the installer with network options replaces them.

## Upgrading

The easy way is **Updates** in the web interface; see [Updates and backups](updates.md).

Running the installer again works too:

```bash
sudo /opt/taper/install.sh --version X.Y.Z   # or the curl command above, for the newest
```

## Backups

Everything Taper keeps is in `/var/lib/taper`: the database, and the files
attached to assignments and work in `files/`. Admins can download both from
**Settings → Download database and files** (a `.tar.gz`), or just the database
with **Database only**. On the server, to copy the database while Taper is
running:

```bash
sudo taper backup /var/lib/taper/backup-$(date +%F).db
```

and copy the uploaded files with them, for example
`sudo tar czf files-$(date +%F).tar.gz -C /var/lib/taper files`.

Then copy those somewhere other than the server, or rely on your VM's snapshot
or backup tool. To restore, stop Taper (`sudo systemctl stop taper`), put the
database in place of `/var/lib/taper/taper.db` and the `files` folder in
`/var/lib/taper/files` (both owned by the `taper` user:
`sudo chown -R taper:taper /var/lib/taper`), and start Taper again. A
`.tar.gz` from Settings unpacks into exactly that layout:
`sudo tar xzf taper-….tar.gz -C /var/lib/taper`.

## Removing Taper

```bash
sudo /opt/taper/uninstall.sh          # keeps the data, so a reinstall picks it up
sudo /opt/taper/uninstall.sh --purge  # deletes everything
```

## Where things are

| Path | What |
|------|------|
| `/opt/taper/` | The program and the installer |
| `/etc/taper/taper.conf` | How Taper is reached, as set by the installer |
| `/var/lib/taper/network.json` | How Taper is reached, if changed in the web interface |
| `/var/lib/taper/taper.db` | The database: people, settings and everything else |
| `/var/lib/taper/files/` | Files attached to assignments and work |
| `/var/lib/taper/backups/` | Database copies from before each update (the last five) |
| `/var/lib/taper/secret.key` | Encrypts saved secrets such as the GitHub token; keep it with backups |
| `/var/lib/taper/certs/` | Let's Encrypt certificates (HTTPS mode) |
| `journalctl -u taper` | The logs |
