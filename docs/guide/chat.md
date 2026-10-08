# Chat

Open **Chat** at the top of any page. A number next to it counts the messages
you haven't read yet (on a phone, a dot on the menu button).

- **Community** is for everyone at your school.
- Each of your classes has a **class chat** for its mentors and scholars. Admins
  can open any class's chat from the class's page.

Each class's page also has a **Class chat** card, with the number of new
messages.

## Talking

Type in the box at the bottom and select **Send**. On a computer, **Enter**
sends and **Shift+Enter** starts a new line; on a phone, use **Send**.

New messages appear straight away, without reloading. If you've scrolled up to
read earlier messages, **New messages ↓** takes you back down. **Show earlier
messages** at the top goes further back.

Web addresses become links. Messages can be up to 2,000 characters.

## Removing a message

Open the **⋯** menu on one of your messages and select **Remove message**.
Everyone then sees "Message removed by its author" in its place.

## Moderating (mentors and admins)

A class's mentors look after its chat; admins look after every chat, including
Community. Open the **⋯** menu on someone's message to:

- **Remove message:** it shows as "Message removed by a moderator".
- **Mute** them **for an hour**, **for a day**, or **until I unmute**. Muted
  people can still read the chat but can't post in it. Muting is per chat:
  someone muted in one class can still post in another.

**Manage** at the top of the chat lists who's muted, with **Unmute**.

Mentors and admins can't be muted in the chats they look after.

## Turning chat off (admins)

Under **Manage**, **Turn off this chat** stops anyone posting, and hides the
chat from scholars (and, for Community, from mentors). Messages are kept. Turn
it back on from the same place: admins can always open a chat that's off, and
a class's mentors can still see their class's.

When a class is archived, its chat is kept, read-only.

## Behind a reverse proxy

New messages arrive over a connection that stays open. Taper tells nginx not to
hold them back, and sends a small keep-alive every 25 seconds, so proxies with
the usual timeouts (60 seconds or more) work without changes. If messages only
show up after reloading, see [Installing Taper](install.md#use-a-reverse-proxy-you-already-have).
