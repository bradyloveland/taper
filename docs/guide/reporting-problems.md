# Reporting a problem

If something in Taper doesn't work, or is confusing, tell the people who make it.

1. Select **Report a problem** at the bottom of any page.
2. Give it a short title, and describe what you were doing, what you expected,
   and what happened instead.
3. Leave **Include technical details** on unless you'd rather not. It adds the
   Taper version, the page you were on, your browser, and your role (scholar,
   mentor or admin). It never adds your name.
4. Select **Send report**.

**Reports are public.** They're posted on GitHub, where anyone can read them.
Don't include names, passwords, or anything private about a scholar or your
school.

## What happens next

If your school has connected Taper to GitHub, your report becomes an issue there
right away, and the page links to it so you can follow it. Otherwise it's saved
for your school's admins. If you have a GitHub account, you can also send it
yourself with **Open it on GitHub**.

## For admins: connecting reports to GitHub

Every report is saved in Taper, and admins can see them under **Settings →
Problem reports**, including who sent each one. To have reports also go to
GitHub as issues:

1. On GitHub, create a
   [fine-grained personal access token](https://github.com/settings/personal-access-tokens/new):
   - **Repository access:** only `bradyloveland/taper` (or your school's copy of Taper).
   - **Permissions:** **Issues: Read and write**. Nothing else.
   - Pick an expiry date you'll remember to renew.
2. In Taper, open **Settings**, paste the token under **Problem reports**, and select
   **Save**. Taper checks that the token works before saving it.

The token is stored encrypted and is never shown again. To replace it, paste a
new one. To stop sending reports, select **Remove the saved token**. Reports
that weren't sent can be sent later from the reports list.
