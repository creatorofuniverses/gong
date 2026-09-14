# Send to chats and forum topics

## Regular chats

With `mode: chat`, `topic` adds a hashtag on a new line. For example,
`nightly backup` becomes `#nightly_backup`. `topic_id` is not valid in this mode.

## Forums

### Set up the group first

Telegram needs a little setup before Gong can create topics:

1. Enable **Topics** in the group's Telegram settings and save. Reopen the
   group and check that topics are actually visible. During our setup, the
   switch needed a second attempt before the change took effect.
2. In **BotFather → Bot Settings → Group Admin Rights**, enable
   **Manage Topics** for your bot.
3. Add your bot as an **administrator**. Open its own entry in the group's
   **Administrators** list and
   enable **Manage Topics**. Check the group's topic permissions too; if the
   option is missing, finish enabling topics and reopen the settings.
4. [Get the chat ID again](chat-id.md) from a fresh message sent after enabling
   topics. Converting a group to a supergroup changes its ID, so the ID you
   copied earlier may no longer point to the right chat.
5. Put that new ID in Gong's config, select `mode: forum`, and restart
   `gong serve`. Changing the YAML alone does not reload a running server.

For a single destination, use `default`:

```yaml
targets:
  default:
    chat_id: "-1001234567890" # Replace with the current supergroup ID.
    mode: "forum"
```

Then try:

```sh
./gong notify --topic backup -- '📦 Your first topic notification'
```

`mode: forum` tells Gong to use topics; it does not enable them in Telegram.

### Send and manage topics

Set a target to `mode: forum`. No topic sends to General. A name creates a topic
on first use and remembers its ID until Gong restarts. A numeric ID goes straight
to an existing topic.

The examples below use a target alias named `operations`; define it under
`targets` first, or omit `--target operations` to use `default`.

```sh
./gong notify --target operations --topic backup -- 'Creates backup on first use'
./gong topics create --target operations --name 'Manual topic'
./gong notify --target operations --topic-id 123456 -- 'Uses a known topic'
```

`topics create` returns an ID but does not add the name to Gong's automatic map.
The HTTP versions are:

```sh
curl --fail-with-body --json '{"target":"operations","name":"Manual topic"}' \
  http://localhost:8080/topics
curl --fail-with-body -X DELETE \
  'http://localhost:8080/topics/123456?target=operations'
```

Delete removes the topic and every message in it. It is not idempotent, and it
does not clear an automatic in-memory mapping:

```sh
./gong topics delete --target operations --id 123456
```

## Permissions

The supergroup must have forums enabled. Creating topics needs
`can_manage_topics` ([Telegram reference](https://core.telegram.org/bots/api#createforumtopic)).
Pinning needs `can_pin_messages` in a group or `can_edit_messages` in a channel
([pinChatMessage](https://core.telegram.org/bots/api#pinchatmessage)). Deleting a
topic needs `can_delete_messages`
([deleteForumTopic](https://core.telegram.org/bots/api#deleteforumtopic)).

### If Telegram rejects the request

Check the bot's actual permissions with the current group ID and the bot token
you used during setup:

```sh
group_id='-1001234567890' # Replace with your current group ID.
curl --silent --show-error --get \
  "https://api.telegram.org/bot${GONG_BOT_TOKEN}/getChatMember" \
  --data-urlencode "chat_id=$group_id" \
  --data-urlencode "user_id=${GONG_BOT_TOKEN%%:*}"
```

Look for `"status":"administrator"` and `"can_manage_topics":true`.
Being an administrator alone is not enough: a bot can have permission to pin
and delete messages while still having `can_manage_topics: false`.

The “has no access to messages” label concerns reading group messages. Gong
only sends events; disabling [privacy mode](https://core.telegram.org/bots/features#privacy-mode)
is not required to create topics. Topic permissions are separate.

## What survives a restart

Telegram keeps topics, but Gong forgets its name-to-ID map. Reusing a name after
a restart creates another topic. Save important `topic_id` values and send them
explicitly. Telegram does not offer Gong a list/search-by-name method, and Gong
has no `list`, `bind`, or `reset` command.

If creation has an uncertain result, Gong blocks that name until restart to
avoid duplicates. Check Telegram, then use a found ID or a new name. Reaching
`max_topics` blocks new names until restart; known names and explicit IDs still
work. Closed or deleted topics are not recreated automatically.

Gong picks an icon from Telegram's catalog using the exact topic name. The same
name stays consistent while the catalog is unchanged. If the catalog cannot be
loaded, Gong uses a standard color instead.
