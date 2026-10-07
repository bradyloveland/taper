# Email

*For admins.* Taper can send email through your school's mail server, or a
service such as Google Workspace, Microsoft 365, Fastmail, Mailgun or Postmark.
Open **Settings → Email**.

## What Taper emails

- **Password reset links.** With email set up, the sign-in page offers "Forgot
  your password?". People who have an email address on their account (under My
  account) get a link that works once, for an hour. People without one still ask
  an admin.
- **Notices to admins**, if turned on: a new version of Taper is out, an update
  was undone because it didn't start, and someone reported a problem. They go to
  admins who have an email address on their account.
- Later versions add notices about assignments and messages. Each person will be
  able to turn those off.

## Setting it up

| Setting | What to enter |
|---------|---------------|
| Server | Your provider's SMTP server, like `smtp.gmail.com` or `smtp.office365.com` |
| Port and security | Usually **587 with STARTTLS**, or 465 with TLS |
| Username and password | The account Taper sends from. For Google and Microsoft, use an *app password*, not the account's normal password |
| From address | The address emails come from, like `taper@yourschool.org` |
| From name | Optional. Your school's name is used if this is empty |

Select **Save**, then **Send test email** to check it works. The password is
stored encrypted and never shown again; leave it empty when saving to keep it.

"None" for security sends the password unencrypted. Use it only for a mail
server on your own network.

## Public address

Links in emails use the **public address** under **Settings → Network & HTTPS**.
Set it if people reach Taper through a reverse proxy or a different name than
the one you're using.
