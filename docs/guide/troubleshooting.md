# Troubleshooting

## Someone forgot their password

An admin opens **People**, selects the person, and chooses **Reset password**.
See [Managing people](people.md#resetting-a-password).

## The only admin is locked out

On the server, give the admin a new temporary password:

```bash
sudo taper passwd USERNAME
```

This also turns the account back on if it was deactivated. If you don't remember
the username, run it with any name, and it lists the admins.

## "Too many tries"

After 5 wrong passwords for an account within 15 minutes, Taper makes that
account wait. Wait the time it shows, or have an admin reset the password, which
clears the wait.

## I lost the setup code

```bash
sudo taper setup-code
```

## An update went wrong

If a new version doesn't start, Taper puts the previous one back on its own; the
**Updates** page explains what happened. You can also go back from that page.
If Taper doesn't come back at all, on the server:

```bash
sudo systemctl stop taper
sudo taper rollback
sudo systemctl start taper
```

This puts back the previous version and the database from just before the
update. See [Updates and backups](updates.md).

## The page doesn't load

1. Check the service is running: `sudo systemctl status taper`
2. Look at the recent log: `sudo journalctl -u taper -n 50`
3. Check the address and port in `/etc/taper/taper.conf`, and that a firewall
   isn't blocking it.
4. In HTTPS mode, check that the DNS name points at the server and that ports 80
   and 443 reach it from the internet. Let's Encrypt can't issue a certificate
   otherwise, and the log says why.

## Behind a proxy, sign-in doesn't stick or forms say "That form expired"

The proxy must pass the original `Host` header and `X-Forwarded-Proto`, and Taper
must trust the proxy's address (`--behind-proxy` or `--proxy-ip`). See
[Installing Taper](install.md#use-a-reverse-proxy-you-already-have).

## "Add to Home Screen" doesn't appear

Phones only offer it for `https://` addresses. See [HTTPS](install.md#https).

## Reporting a problem

Use **Report a problem** at the bottom of any page (see
[Reporting a problem](reporting-problems.md)), or open an issue on
[GitHub](https://github.com/bradyloveland/taper/issues). Don't include names or
other private details about scholars, because issues are public.

## "Not sent" problem reports

The GitHub token may have expired or lost its permission. An admin can add a new
one under **Settings → Problem reports**, then send the reports again from the
reports list.
