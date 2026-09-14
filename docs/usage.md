# Send events

These examples use a running Gong at `http://localhost:8080`. See
[Configuration](configuration.md#config-discovery) for another address.

## curl

Check the process without contacting Telegram:

```sh
curl --fail --silent --show-error http://localhost:8080/health
```

Send JSON:

```sh
curl --fail-with-body --silent --show-error \
  --json '{"message":"<b>Backup</b> finished","target":"default","topic":"backup","level":"success","category":"result"}' \
  http://localhost:8080/notify
```

Or stream plain text and keep line breaks:

```sh
printf 'First line\nSecond line\n' | \
  curl --fail-with-body --silent --show-error \
    -H 'Content-Type: text/plain; charset=utf-8' \
    -H 'X-Target: default' \
    -H 'X-Topic: nightly backup' \
    -H 'X-Level: success' \
    -H 'X-Category: result' \
    --data-binary @- http://localhost:8080/notify
```

For an authenticated API:

```sh
export GONG_API_TOKEN='YOUR_API_TOKEN'
curl --fail-with-body --silent --show-error \
  -H "Authorization: Bearer ${GONG_API_TOKEN}" \
  --json '{"message":"Protected event"}' http://localhost:8080/notify
```

## CLI

Flags go after `notify` and before the message. With no message argument, Gong
reads stdin:

```sh
./gong notify --target default --topic backup --level success \
  --category result -- '<b>Backup</b> finished'
printf 'First line\nSecond line\n' | ./gong notify --topic multiline
```

The client timeout defaults to 30 seconds. `--url` takes an origin with no path:

```sh
./gong notify --url http://localhost:8080 --timeout 30s -- 'Hello'
```

JSON goes to stdout. Exit code `0` is full success, `1` is failed delivery or a
pin failure after delivery, and `2` is invalid CLI input.

## Format your messages

Emoji work out of the box, and Gong sends messages with Telegram HTML formatting
enabled by default. Add a bold heading, a link to the results, or a code snippet:

```sh
./gong notify -- '✅ <b>Backup complete</b>
<code>photos.tar.gz</code> · 2.4 GB
<a href="https://example.com/backups">View backups</a>'
```

The same formatting works with curl:

```sh
curl --fail-with-body --json \
  '{"message":"🚀 <b>Deployment complete</b>\nVersion: <code>v1.2.3</code>"}' \
  http://localhost:8080/notify
```

Use [Telegram's supported HTML tags](https://core.telegram.org/bots/api#html-style).
For line breaks, use actual newlines (or `\n` in JSON). Escape literal `<`, `>`
and `&` in values you insert into HTML as `&lt;`, `&gt;` and `&amp;`.
If Telegram rejects the formatting, Gong sends the original text as plain text
by default. Set `fallback_plain_text: false` (CLI: `--fallback-plain-text=false`)
to report the formatting error instead.

## Request fields

| JSON | Plain-text header | Value |
|---|---|---|
| `message` | request body | Nonempty UTF-8; treated as HTML by default. |
| `target` | `X-Target` | Chat alias; default `default`. |
| `topic` | `X-Topic` | Optional forum name or normal-chat hashtag. |
| `topic_id` | `X-Topic-ID` | Positive forum topic ID; cannot be combined with `topic`. |
| `level` | `X-Level` | `debug`, `info`, `success`, `warning`, or `error`. |
| `category` | `X-Category` | Your label for pinning rules. |
| `fallback_plain_text` | `X-Fallback-Plain-Text` | Retry invalid HTML as plain text; default `true`. |

Unknown JSON fields are rejected. A normal-chat response looks like:

```json
{"ok":true,"message_id":42,"level":"info","silent":true,"fallback_used":false,"target":"default","pin_status":"not_requested"}
```

Forum responses may include `topic_id`. `pin_status` is `not_requested`,
`pinned`, or `failed`; a failure also includes `pin_error`.

## Handle failures safely

Gong sends synchronously and has no queue. A timeout or lost response may carry
`"uncertain": true`: Telegram could have completed the request. Check the chat
before retrying. If `pin_status` is `failed`, the message was already delivered;
retrying the notification would duplicate it.

Gong does not automatically retry mutations. The one exception is a confirmed
HTML parse error: with `fallback_plain_text: true`, it sends the same text once
without HTML mode.

Useful HTTP statuses include `401` for API auth, `413` for a body over 64 KiB,
`422` for request fields, `429` for Telegram rate limiting, `502` for Telegram
failure, and `504` for timeout. `409 topic_creation_uncertain` and
`503 topic_capacity_exhausted` still allow an explicit known `topic_id`.

## Wrap shell commands

The helpers call `gong` by name, so first put the extracted directory on PATH.
For a local source build, use `$PWD/bin` instead:

```sh
export PATH="$PWD:$PATH"
```

Then source the helper for your shell:

```bash
source examples/shell/gong.bash
gong_notify --level info -- 'Hello'
gong_backup 'Nightly backup' tar -czf '/tmp/backup file.tar.gz' README.md
```

Zsh uses `source examples/shell/gong.zsh`. For Fish, run `fish_add_path $PWD`
(or `$PWD/bin` for a source build), then `source examples/shell/gong.fish`.
To keep the setup, use `~/.bashrc`, `~/.zshrc`, or
`~/.config/fish/config.fish`. You can also
[install Gong on your PATH](installation.md#put-gong-on-your-path).
`gong_notify_after`, `gong_backup`, and `gong_training` run the command without
`eval`, preserve arguments, escape the label for HTML, and always return the
original command's exit code. A notification failure never hides that result.

## Moving from tg-ntfy

`POST /notify`, JSON and text bodies, HTML fallback, levels, and `/health` stay
compatible. Rename old settings explicitly:

| tg-ntfy | Gong |
|---|---|
| `TELEGRAM_BOT_TOKEN` | `telegram.bot_token` or `GONG_BOT_TOKEN` |
| `TELEGRAM_CHAT_ID` | `targets.default.chat_id` |
| `NOTIFY_MIN_LEVEL` | `notify_min_level` |
| `PORT` | `listen` |

Restarting only loses temporary forum name mappings. Telegram keeps the chats,
topics, and messages.
