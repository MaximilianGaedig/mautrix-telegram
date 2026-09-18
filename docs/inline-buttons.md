# Telegram keyboards / buttons

The bridge converts every kind of Telegram message keyboard (`ReplyMarkup`) into a custom Matrix event content
field, and provides a `click` bridge command to press the buttons that can be pressed from Matrix.

This covers the bot service account (777000) "Reject Group Transfer" style messages, as well as ordinary bot
keyboards (inline keyboards, custom reply keyboards, "hide keyboard" and "force reply").

## JSON schema

When a Telegram message has a `ReplyMarkup`, the bridge adds a `fi.mau.telegram.buttons` field to the top level
of the Matrix event content (the name is kept for API stability even though it now covers more than inline
keyboards):

```json
{
  "msgtype": "m.text",
  "body": "Group Transferred to You...",
  "fi.mau.telegram.buttons": {
    "message_id": 12345,
    "keyboard": "inline",
    "rows": [
      [
        { "text": "Reject Group Transfer", "type": "callback", "command": "!tg click 12345 0 0" },
        { "text": "Docs", "type": "url", "url": "https://example.com" }
      ],
      [
        { "text": "Whatever", "type": "unsupported" }
      ]
    ]
  }
}
```

Top-level fields:

* `message_id` — the Telegram message ID the keyboard belongs to. This is always the first argument of the
  `click` command.
* `keyboard` — one of:
  * `inline` — a `replyInlineMarkup` (normal bot inline keyboard under the message).
  * `reply` — a `replyKeyboardMarkup` (a custom keyboard replacing the client's own keyboard). Also has
    `resize`, `single_use`, `placeholder` (string, optional) and `selective` (bool).
  * `hide` — a `replyKeyboardHide` (asks the client to hide any custom reply keyboard). No `rows`.
  * `force_reply` — a `replyKeyboardForceReply` (asks the client to open the composer in reply mode). Also has
    `single_use`, `placeholder` and `selective`. No `rows`.
* `rows` — for `inline` and `reply` keyboards, every row of the **original** Telegram keyboard, including
  buttons that can't be pressed through the bridge. This keeps `row`/`col` indexes in `command` values valid:
  they always refer to a position in the original Telegram keyboard, not a filtered-down one.

Every button has `text` and `type`. Depending on `type`:

| `type`           | Extra fields                              | Meaning / how to act on it |
|-------------------|--------------------------------------------|-----------------------------|
| `callback`        | `command`, `requires_password` (optional) | `KeyboardButtonCallback`. Send `command` to press it. If `requires_password` is `true`, pressing it always fails (see Limitations). |
| `url`             | `url`                                      | `KeyboardButtonUrl` (or a `KeyboardButtonUrlAuth` without a bridgeable auth flow — see below). Just a link. |
| `url_auth`        | `url`, `command`                          | `KeyboardButtonUrlAuth`. `url` is the plain fallback link; sending `command` makes the bridge complete the Telegram login-widget handshake and reply with the authorized URL. |
| `reply`           | —                                           | Plain `KeyboardButton` inside a `reply` keyboard. No command: the client should send `text` as an ordinary message to the room, which the bridge forwards to Telegram like any other message. |
| `copy`            | `copy_text`                                | `KeyboardButtonCopy`. Purely client-side: copy `copy_text` to the clipboard. No command. |
| `switch_inline`   | `query`, `same_peer`, `bot_username` (optional) | `KeyboardButtonSwitchInline`. Client-side: prefill the composer with `@<bot_username> <query>` (omit the mention if `bot_username` is empty). No command — sending inline query results is not implemented (see mautrix/telegram#364). |
| `user_profile`    | `user_mxid` (optional)                    | `KeyboardButtonUserProfile`. Client-side: link to the ghost's profile. No command. |
| `game`            | `command`                                  | `KeyboardButtonGame`. Sending `command` asks the bot for a game URL and replies with it (see Limitations). |
| `webview`         | `command`                                  | `KeyboardButtonWebView`. Sending `command` asks Telegram for a Mini App URL and replies with it (see Limitations). |
| `simple_webview`  | `command`                                  | `KeyboardButtonSimpleWebView`. Same as `webview`, different underlying RPC. |
| `request_phone`   | `command`                                  | `KeyboardButtonRequestPhone`. Sending `command` shares the logged-in user's own contact card in the chat, like Telegram apps do. |
| `request_geo`     | `command`                                  | `KeyboardButtonRequestGeoLocation`. `command` only replies with instructions (send a location message instead); the bridge does not source a location on its own. |
| `request_poll`    | `command`                                  | `KeyboardButtonRequestPoll`. Same treatment as `request_geo`: reply with instructions to send a poll instead. |
| `request_peer`    | `command`                                  | `KeyboardButtonRequestPeer`. `command` replies that this isn't supported through the bridge; picking and sharing a peer needs Telegram's own UI. |
| `unsupported`     | —                                           | `KeyboardButtonBuy` (payments) and anything else, including `url`/`url_auth` buttons whose URL isn't `http(s)`/`tg:`. Text only. |

`command` always uses the bridge's configured command prefix (`bridge.command_prefix`, e.g. `!tg`), not a
hard-coded value.

### Multi-part messages

For albums/media-with-caption, the field is attached to the part that carries the text (the merged
media+caption part after `ConvertedMessage.MergeCaption()`, i.e. `cm.Parts[0]`), since that's the only part
that gets updated by edits too.

### Edits

Telegram bots usually edit a message to remove or replace its keyboard after a button is pressed. The keyboard
is included in the message's content hash (`MessageMetadata.ContentHash`), so an edit that changes only the
keyboard is not dropped as a no-op. The field is added through `ConvertedEditPart.Extra`, which bridgev2 puts
into `m.new_content`; when the new message has no keyboard, `applyInlineKeyboard` simply doesn't set the field,
so the edit's `m.new_content` has no `fi.mau.telegram.buttons` key at all.

## `inline_button_fallback` config option

When `inline_button_fallback: true` (default `false`), the bridge also appends a compact trailing paragraph to
`body`/`formatted_body` listing the buttons: URL/url_auth buttons as `label (url)` / `<a href=...>`, and any
button with a `command` as `label (command)`. This makes buttons at least readable/copy-pasteable in clients
that don't render `fi.mau.telegram.buttons`. `hide` and `force_reply` keyboards have no buttons, so they never
produce fallback text.

## The `click` command

```
!tg click <message ID> <row> <column>
```

* Requires being in a portal room and logged in (`RequiresPortal`, `RequiresLogin`).
* Row/column are zero-based indexes into the keyboard **as it currently exists on Telegram** — the command
  re-fetches the message (`messages.getMessages`/`channels.getMessages`, same split as direct media downloads)
  rather than trusting the keyboard that was bridged, since bots often change their keyboards.
* Arguments are validated strictly: non-numeric, negative, too large (`>2^31-1`), or the wrong number of
  arguments all produce a friendly usage error instead of touching the Telegram API.
* Errors from Telegram are passed through `humanise.Error`, with extra handling for `FLOOD_WAIT`,
  `BOT_RESPONSE_TIMEOUT`, and RPC timeouts.
* On success, the bot's answer (if the button type produces one) is replied as a plain-text message
  (marking alerts) or a URL to open; when there's nothing to say, the command reacts with `✅` on the command
  message instead of posting a new one.

## Manual testing against a real Telegram account

1. Set `inline_button_fallback: true` temporarily so buttons are visible without an Element-side renderer.
2. Have another account (or a bot you control, e.g. via @BotFather) send a message with an inline keyboard to a
   chat with the bridge user, or trigger a real "Group Transferred to You" message from the 777000 service
   account.
3. Confirm the Matrix event has the `fi.mau.telegram.buttons` field (`/devtools` in Element, or `message_id`
   field via any Matrix client that shows raw event JSON) with the expected `rows`.
4. Send `!tg click <message_id> 0 0` in the portal room and confirm the command replies with the bot's answer
   (for `callback`) or the fetched URL (for `url_auth`/`game`/`webview`/`simple_webview`), or that a contact
   card is sent to the chat (for `request_phone`).
5. Edit the source message from the bot side (e.g. by pressing the button from a real Telegram client) and
   confirm the Matrix edit either updates or removes the `fi.mau.telegram.buttons` field as appropriate.
6. For a `reply` keyboard, send the same text as a plain button back into the room and confirm it's bridged to
   Telegram as a normal message (no special handling needed — the button's `text` is just what gets sent).

## Known limitations / deviations from a "real" Telegram client

* **Custom reply keyboards were previously out of scope entirely** (mautrix/telegram#638, "bot custom reply
  keyboards not bridged"). This change adds the `reply`/`hide`/`force_reply` schema and forwards a plain
  `reply` button's text as an ordinary message, but there is no special "keyboard UI" on the Matrix side unless
  a client renders `fi.mau.telegram.buttons` — without that, the user has to type the button's exact text.
* **`requires_password` callback buttons** (2FA/SRP-protected, used e.g. by @BotFather for bot ownership
  transfer) cannot be pressed through the bridge at all: there is no way to collect the Telegram 2FA password
  and produce an SRP proof through a text command. `click` always fails with a clear error for these.
* **`url_auth` buttons** are pressed with `messages.requestUrlAuth` followed immediately by
  `messages.acceptUrlAuth` with `write_allowed: false`. A real Telegram client shows a confirmation dialog
  (bot name/domain, whether to allow the bot to send messages) before accepting; the bridge can't show that
  dialog, so it always accepts without granting write access. If the bot's flow requires write access to work,
  this will not fully succeed — the fallback plain URL is returned in the reply message either way.
* **`game`/`webview`/`simple_webview` buttons** get a URL back from Telegram, but that URL is a Telegram Mini
  App page that expects the `Telegram.WebApp` JavaScript bridge injected by real Telegram clients. Opening it
  in a normal browser will generally show a broken or non-functional page. The URL is still returned so
  advanced users can inspect or work around it manually.
* **`request_geo`/`request_poll`/`request_peer` buttons** are not automated — the bridge only replies with
  instructions (send a location/poll message normally, or use the official app for peer sharing). Implementing
  the underlying Telegram calls (posting a live/static location, building a poll, or picking+creating a peer
  via `messages.sendBotRequestedPeer`) was judged not worth the complexity/risk for a text-command UI; this can
  be revisited if there's demand.
* **`switch_inline` and `user_profile` buttons** are intentionally command-less: they're meant to be handled by
  the Matrix client UI (prefill composer / open profile), not by sending a bridge command. `bot_username` may
  be empty if the bot's username hasn't been seen/cached by the bridge yet.
* **`buy` buttons** (Telegram Payments) are always `unsupported`; in-bridge payments are out of scope.
* Row/column indexes are **only** valid against the *current* Telegram keyboard, not necessarily the one that
  was bridged to Matrix — always let `click` re-fetch rather than assuming a stale bridged keyboard still
  matches.
