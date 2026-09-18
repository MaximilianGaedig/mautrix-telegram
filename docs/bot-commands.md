# Telegram bot commands

Telegram bots publish a list of `/commands` that Telegram clients offer as completions. The bridge mirrors
those lists into portal rooms as a state event, so Matrix clients can offer the same completion.

## State event

Type `fi.mau.telegram.bot_commands`, state key `""`, sent by the bridge bot.

```json
{
  "bots": [
    {
      "bot": "@telegram_123456:example.com",
      "username": "examplebot",
      "commands": [
        {"command": "start", "description": "Start the bot"},
        {"command": "help", "description": "Show help"}
      ]
    }
  ]
}
```

* `bots`: one entry per bot in the chat that has at least one command. Sorted by username, bots without
  a username first. Always present, possibly empty (an empty list means the bots' commands were removed
  or the bots left).
  * `bot`: the Matrix ID of the bot's ghost user.
  * `username`: the bot's Telegram username without `@`. Omitted if the bot has none.
  * `commands`: `command` has no leading `/`; `description` may be empty.

### DM portals

In a DM with a bot, the content additionally keeps the original top-level fields, so clients written for
the first version of this event keep working:

```json
{
  "bot": "@telegram_123456:example.com",
  "commands": [{"command": "start", "description": "Start the bot"}],
  "bots": [{"bot": "@telegram_123456:example.com", "username": "examplebot", "commands": [...]}]
}
```

New clients should read `bots` in every room and ignore the top-level `bot`/`commands`.

### Group and channel portals

Basic groups and supergroups/channels get the event from the `bot_info` list of the full chat
(`messages.getFullChat` / `channels.getFullChannel`). It has no top-level `bot`/`commands`.

## Sending a command

* In a DM, send `/command` (optionally followed by arguments).
* In a group, Telegram expects `/command@username` whenever more than one bot is present. Clients
  should complete to `/command@username` in groups when `username` is set. Plain `/command` is
  delivered to every bot that has that command.

## Updates

The bridge re-sends the event when the chat info is resynced and the content (or the portal room)
changed. It doesn't send one for chats that never had bots with commands.
