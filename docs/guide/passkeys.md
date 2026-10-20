# Passkeys

A passkey lets you sign in with your fingerprint, your face or your device's
PIN, instead of typing your password. Your phone or computer keeps it, and it
only works on your school's Taper, so it can't be stolen by a fake sign-in page.

A passkey counts as [two-step sign-in](two-step.md): the device checks it's
you, so there's no code to type.

## Adding a passkey

1. Open **My account → Passkeys → Add a passkey**.
2. Select **Add a passkey on this device**, and follow what your device asks:
   your fingerprint, face or PIN.

On an iPhone, iPad or Mac, passkeys sync through iCloud Keychain, and on
Android through your Google account, so one passkey usually works on all your
devices. Those show **Synced**. On a shared or school computer, add one on your
phone instead: the computer can show a QR code to sign in with your phone.

Taper needs a secure (https) address for passkeys. A passkey belongs to the
address it was made on, so if your school moves Taper to a new address, add
your passkeys again there.

## Signing in

On the sign-in page, select **Sign in with a passkey**. On many devices, your
passkey is also offered when you tap the username box. Tick **Keep me signed
in** first if it's your own device.

## If your school requires two-step sign-in

A passkey is enough: you won't be asked to set up a code too. With a passkey
and no code, your password alone doesn't sign you in; use your passkey.

## Lost or stolen devices

Open **My account → Passkeys** on another device and **Remove** the lost one's
passkey. If you can't sign in at all, an admin or board member can open your
page under People and select **Remove passkeys**; you then sign in with your
password (and code, if you have one).

If the only admin is locked out, on the server run
`sudo taper mfa-reset USERNAME`. It turns off two-step sign-in and removes
their passkeys.
