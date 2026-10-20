# Chat

Open **Chat** at the top of any page. A number next to it counts the messages
you haven't read yet (on a phone, a dot on the menu button).

- **Community** is the whole school's chat. Everyone can read it; admins and
  board members post in it, so it stays a place for news and announcements.
- Each of your classes has a **class chat** for its mentors and scholars.
- **Group chats** have their own name and members, such as a committee, a
  project team or the parents of a class. Admins and board members make them.

Admins and board members can open every chat: those they're not in are listed
under **Other chats**, and only their own chats count towards the number next
to Chat. Each class's page also has a **Class chat** card, with the number of
new messages.

## Talking

Type in the box at the bottom and select **Send**. On a computer, **Enter**
sends and **Shift+Enter** starts a new line; on a phone, use **Send**.

New messages appear straight away, without reloading. If you've scrolled up to
read earlier messages, **New messages ↓** takes you back down. **Show earlier
messages** at the top goes further back.

Web addresses become links. Messages can be up to 2,000 characters.

## Photos and files

Select **＋** next to the box to attach photos or files (up to 10 at a time,
25 MB each). They show above the box; select **×** to take one off before
sending. On a computer you can also paste a picture straight into the box.

Photos show in the chat; select one to see it full size. Other files download.
Only people who can see the chat can open its files.

## Removing a message

Open the **⋯** menu on one of your messages and select **Remove message**.
Everyone then sees "Message removed by its author" in its place, and its photos
and files are deleted.

## Moderating (mentors, the board and admins)

A class's mentors look after its chat; admins and board members look after
every chat. Open the **⋯** menu on someone's message to:

- **Remove message:** it shows as "Message removed by a moderator".
- **Mute** them **for an hour**, **for a day**, or **until I unmute**. Muted
  people can still read the chat but can't post in it. Muting is per chat:
  someone muted in one class can still post in another.

**Manage** at the top of the chat lists who's muted, with **Unmute**.

Admins and board members can't be muted, and nor can a class's mentors in its
chat.

## Group chats (admins and the board)

On the Chat page, select **New group chat**, give it any name, tick the people
to include (type to find them), and select **Create chat**. You're in it too.

In the chat, **People** under its name (or **Manage → People, name and
delete**) lets you add and remove people, rename it, or delete it with all its
messages. Members can talk in it; only admins and board members change it.

## Turning chat off (admins and the board)

Under **Manage**, **Turn off this chat** stops anyone posting and hides the
chat from everyone but admins and board members (and, for a class, its
mentors). Messages are kept. Turn it back on from the same place.

When a class is archived, its chat is kept, read-only.

## Behind a reverse proxy

New messages arrive over a connection that stays open. Taper tells nginx not to
hold them back, and sends a small keep-alive every 25 seconds, so proxies with
the usual timeouts (60 seconds or more) work without changes. If messages only
show up after reloading, or attachments won't upload, see
[Installing Taper](install.md#use-a-reverse-proxy-you-already-have).
