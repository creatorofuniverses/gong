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

## Wrap shell commands

### Copy a small function

First [put Gong on your PATH](installation.md#put-gong-on-your-path).
For Bash or Zsh, paste this into your terminal:

```sh
gong_notify() { command gong notify "$@"; }
gong_notify -- 'Backup finished'
```

Keep the function in `~/.bashrc` (Bash) or `~/.zshrc` (Zsh) to use it in new
terminals. For Fish, put this in `~/.config/fish/config.fish`:

```fish
function gong_notify
    command gong notify $argv
end
```

Open a new Fish terminal, then send a message:

```fish
gong_notify -- 'Backup finished'
```

The function passes arguments through to the CLI, so you can add options as you
need them:

```sh
gong_notify --level success --topic backup -- 'Backup finished'
```

Messages support [HTML formatting](#format-your-messages); escape dynamic text
before inserting it into HTML. These functions use the CLI's config discovery
and environment variables, and return its exit status.

### Notify after a successful command

For a quick one-off job in Bash or Zsh:

```sh
tar -czf /tmp/backup.tar.gz README.md && gong_notify -- 'Backup finished'
```

This only sends a message when `tar` succeeds. For both success and failure,
elapsed time, and the original command's exit status, use the helpers below.

### Install the ready-made helper set

The included helpers save you from writing command-status handling yourself.
Copy the file for your shell from the extracted release or source checkout.
For Bash:

```sh
mkdir -p "$HOME/.local/share/gong"
cp examples/shell/gong.bash "$HOME/.local/share/gong/gong.bash"
source "$HOME/.local/share/gong/gong.bash"
```

Add `source "$HOME/.local/share/gong/gong.bash"` to `~/.bashrc` for future
terminals. For Zsh, copy [gong.zsh](../examples/shell/gong.zsh) to
`~/.local/share/gong/gong.zsh` and add
`source "$HOME/.local/share/gong/gong.zsh"` to `~/.zshrc`.

For Fish, copy [gong.fish](../examples/shell/gong.fish) to
`~/.local/share/gong/gong.fish` and add
`source "$HOME/.local/share/gong/gong.fish"` to `~/.config/fish/config.fish`.
Source it in your current terminal too, or open a new one.

The helper set includes the same `gong_notify` function plus wrappers:

| Function | What it does |
|---|---|
| `gong_notify` | Sends a message with the CLI options you provide. |
| `gong_notify_after` | Runs a command, then reports success or failure and elapsed time in `result`. |
| `gong_backup` | The same command wrapper, using the `backup` topic. |
| `gong_training` | The same command wrapper, using the `training` topic. |

Pass a readable label, followed by the command and its arguments:

```sh
gong_notify_after 'Archive README' tar -czf /tmp/readme.tar.gz README.md
gong_backup 'Nightly backup' tar -czf '/tmp/backup file.tar.gz' README.md
gong_training 'Train model' python3 train.py
```

Replace the command with your own job. Topic names become hashtags in a normal
chat or [topics in a forum](topics.md). The wrappers preserve arguments, escape
the label for HTML, and return the original command's exit code. A notification
failure never hides that result. See the [Bash implementation](../examples/shell/gong.bash)
if you want to customize the helpers.

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
