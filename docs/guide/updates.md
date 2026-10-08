# Updates and backups

*For admins.* Open **Settings → Updates** (Settings is in the menu under your
initials, at the top right).

## Installing a new version

Taper checks GitHub once a day for a new version. When one is out, admins see a
note on the home page.

1. On the **Updates** page, read what's new. Select **Check now** to look right away.
2. Select **Download**. Taper downloads the release and checks that it's signed
   by the Taper project. Anything else is refused.
3. Select **Install**. Taper backs up the database, swaps in the new version and
   restarts. This takes about a minute, and the page continues on its own.

People using Taper at that moment see it pause briefly. Their work is kept, but
pick a quiet time if you can.

Nothing is ever installed without an admin choosing to. To stop the daily check,
turn off **Check for new versions every day**.

## If something goes wrong

- **The new version doesn't start:** Taper puts the previous version back on its
  own, with the database as it was just before the update. The Updates page says
  what happened.
- **The new version starts, but something isn't right:** select **Go back to
  …** on the Updates page. The database goes back to how it was just before the
  update, so **changes made since then are lost**. Please also
  [report the problem](reporting-problems.md).
- **Taper doesn't come back at all:** on the server, run
  `sudo systemctl stop taper`, then `sudo taper rollback`, then
  `sudo systemctl start taper`. See [Troubleshooting](troubleshooting.md).

## Servers without internet access

Download the release file for your server (for most servers,
`taper-<version>-linux-amd64.tar.gz`) from the
[releases page](https://github.com/bradyloveland/taper/releases) on another
computer. Then, on the Updates page, choose it under **Install from a file** and
select **Upload and check**.

## Backups

- Before every update, Taper saves a copy of the database in
  `/var/lib/taper/backups/`. It keeps the last five.
- **Settings → Download database and files** (also linked at the bottom of the
  Updates page) downloads the whole database and every file attached to
  assignments and work, as one `.tar.gz`, at any time. **Database only** leaves
  out the files. Keep backups somewhere safe and private: they hold everyone's
  accounts and work.
- Updates don't touch the uploaded files, so the copies made before an update
  are of the database only.
- From the server: `sudo taper backup /var/lib/taper/backup-$(date +%F).db`

To restore a backup, see [Installing Taper](install.md#backups).

## Updating with the installer

Running the installer again also updates Taper, and it's the only way to change
how Taper is reached (HTTPS, port, proxy). See [Installing Taper](install.md#upgrading).
A version installed this way can't be undone from the Updates page.
