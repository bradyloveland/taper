# Two-step sign-in

Two-step sign-in adds a code from an app on your phone to your password. Someone
who learns your password still can't get in without your phone.

## Turning it on

1. Install an authenticator app on your phone: Google Authenticator, Microsoft
   Authenticator, 1Password, Authy and others all work.
2. In Taper, open **My account → Two-step sign-in → Set up two-step sign-in**.
3. In the app, add an account and scan the QR code. If you can't scan it, choose
   "enter a setup key" and type the letters shown under the code.
4. Enter the 6-digit code the app shows, and select **Turn on two-step sign-in**.
5. **Save your recovery codes.** Taper shows 10 of them once. Print them, or
   keep them in a password manager. Each one works once, in place of a code from
   your phone.

Turning it on signs you out on your other devices. Sign in there again with a
code.

## Signing in

After your password, Taper asks for the 6-digit code from the app. Codes change
every 30 seconds, and each one works only once. If you chose **Keep me signed
in**, you won't be asked again on that device for 30 days.

## Lost your phone?

- **You have your recovery codes:** enter one in the code box when you sign in.
  Then, under **My account → Two-step sign-in**, make new recovery codes, or turn
  two-step sign-in off and set it up again on your new phone.
- **No recovery codes:** ask an admin at your school. They can turn off two-step
  sign-in for you from your page under **Settings → People**, so you can sign in with just
  your password.

## For admins

### Requiring it

Under **Settings → Two-step sign-in**, you can require it for admins, mentors
and/or scholars. People in those roles who haven't set it up are taken to the
setup page when they next sign in, and can't turn it off. Young scholars may not
have phones, so think twice before requiring it for them.

### Turning it off for someone

Open the person under **Settings → People** and select **Turn off two-step sign-in**. They're
signed out everywhere and can sign in with just their password. If their role
requires it, they set it up again at their next sign-in.

### If the only admin is locked out

On the server:

```bash
sudo taper mfa-reset USERNAME
```
