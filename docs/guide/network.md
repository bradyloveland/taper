# Network and HTTPS

*For admins.* Open **Settings → Network & HTTPS**.

## How Taper is reached

- **Plain HTTP:** fine for trying Taper out on your own network. Phones won't
  install the app, and passwords cross the network unencrypted.
- **HTTPS with a free Let's Encrypt certificate:** the simplest real setup.
  Create a DNS name (like `learn.yourschool.org`) pointing at the server, make
  sure ports **80 and 443** reach it from the internet (forward them on the
  router), and enter the name. Taper gets the certificate and renews it by itself.
- **Behind a reverse proxy:** if you already run nginx, Caddy, Nginx Proxy Manager
  or similar for HTTPS. Set the port the proxy forwards to and the proxy's address
  under **Trusted proxies**. If the proxy runs on the same server, use listen
  address `127.0.0.1`. See [Installing Taper](install.md#use-a-reverse-proxy-you-already-have)
  for proxy examples.

## Changes are tried first

A mistake here could lock everyone out, so changes to the address, port or HTTPS
don't take effect straight away:

1. When you select **Save**, Taper starts listening with the new settings **as
   well as** the old ones.
2. Open Taper at the new address (the page gives you the link), and select
   **Keep the new settings**. That proves they work. The old address then stops
   working.
3. If nobody confirms within a few minutes, the new settings are undone by
   themselves. You can also select **Cancel**.

Changes that don't move Taper, such as the trusted proxies, are saved straight
away.

If you're still locked out, on the server run `sudo taper network --reset`,
then `sudo systemctl restart taper`. That goes back to the settings from the
installer.

## Public address

The address people type to reach Taper, such as `https://learn.yourschool.org`.
Taper uses it in links it sends, such as password reset emails and (in a later
version) calendar subscriptions. Leave it empty to use whatever address the
browser used, which is right unless people reach Taper through a reverse proxy
or under a different name.
