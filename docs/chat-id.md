# Find your Telegram chat ID

Use your own bot and send a fresh command while `getUpdates` is waiting.

## Private chat or group

Export your bot token, then start a 60-second poll:

```sh
export GONG_BOT_TOKEN='YOUR_BOT_TOKEN'
curl --silent --show-error --get \
  "https://api.telegram.org/bot${GONG_BOT_TOKEN}/getUpdates" \
  --data-urlencode 'timeout=60' \
  --data-urlencode 'allowed_updates=["message"]'
```

While it waits:

- Private chat: press **Start** or send `/start` to your bot.
- Group: add the bot, then send `/start@BOT_USERNAME` in that group.

For a private chat, the response looks like this (with your own ID):

```json
{
  "ok": true,
  "result": [{
    "message": {
      "from": {"id": 123456789, "first_name": "Alex"},
      "chat": {"id": 123456789, "first_name": "Alex", "type": "private"},
      "text": "/start"
    }
  }]
}
```

Copy the number inside `message.chat.id` into your config:

```yaml
targets:
  default:
    chat_id: "123456789"
```

That's your destination ID. In a private conversation with your bot, it matches
your user ID (`from.id`), so seeing the same number twice is normal. The JSON
field is just called `id`; `message.chat.id` means “the `id` inside `chat`,
inside `message`.” Gong's config calls this setting `chat_id`.

For a group, look for the same field, but expect a negative number:

```json
"chat": {"id": -1001234567890, "title": "Alerts", "type": "supergroup"}
```

Use `chat_id: "-1001234567890"` in that case, including the minus sign.
The group's `chat.id` differs from `from.id`, which identifies the person who
sent the command.

Planning to use topics? [Enable them first](topics.md#set-up-the-group-first),
then copy the ID from a fresh message. Converting a group to a supergroup
changes its ID; Telegram also reports the new ID as
[`migrate_to_chat_id`](https://core.telegram.org/bots/api#message) in a migration
service message. Replace the old `chat_id` in Gong's config and restart the
server. Do not reuse an ID from an older update.

`allowed_updates` is explicit because Telegram remembers the previous filter
when it is omitted. There is no `offset`, so this lookup does not acknowledge
the update; your regular receiver may see it again. See Telegram's
[getUpdates reference](https://core.telegram.org/bots/api#getupdates).

## If the result is empty

`{"ok":true,"result":[]}` means no matching update arrived during that call.
Start the poll first, then send a new command. Also check that you used the
right bot, added it to the group, and addressed the group command to it. Old
updates expire after 24 hours, and another receiver may already have confirmed
one ([getting updates](https://core.telegram.org/bots/api#getting-updates)).
Gong only sends messages and does not consume updates; pause any other
long-poll receiver while you do this lookup.

If polling reports a webhook conflict or stays empty, check whether the bot
already uses a webhook:

```sh
curl --silent --show-error \
  "https://api.telegram.org/bot${GONG_BOT_TOKEN}/getWebhookInfo"
```

A nonempty `result.url` means updates go to that webhook and `getUpdates` is
unavailable. Read `message.chat.id` from the webhook receiver instead.
`pending_update_count` and `last_error_message` can reveal delivery trouble.
This check does not change or remove the webhook
([getWebhookInfo](https://core.telegram.org/bots/api#getwebhookinfo)).

Do not delete an existing webhook or drop pending updates just to find an ID.
Using your own bot also avoids adding a third-party ID bot to a private group.
