<picture>
  <source media="(prefers-color-scheme: dark)"
          srcset="assets/gong-dark.png">
  <source media="(prefers-color-scheme: light)"
          srcset="assets/gong-light.png">
  <img alt="gong — A little nudge when your job is done"
       src="assets/gong-light.png" width="100%">
</picture>

Send events to Telegram. One binary, one config, your bot.

With your bot configured, start Gong and send from another terminal:

```sh
./gong serve
# In another terminal:
curl --fail-with-body -H 'Content-Type: text/plain' \
  --data-binary "🔔 It's gong!" http://localhost:8080/notify
```

That's it — your first notification is in Telegram!

## Why it exists

Gong is inspired by [ntfy.sh](https://ntfy.sh) and [Gotify](https://gotify.net): simple, powerful tools for sending notifications from anywhere.

I wanted something small that sends messages straight to Telegram, where I already spend time. So I built Gong: a little service you can call from a terminal, a script, an app — whatever you're working on.

Don't let the `curl` commands put you off. They're just an easy way to try it out. You can send events with everyday command-line tools or your favorite language's HTTP library.

## Disclaimer

Gong was "vibe coded" from the ground up, with Codex as its tireless builder and Claude as its meticulous reviewer.
It's still early, so expect a few rough edges. Bug reports and contributions are welcome ❤️

## Quick start

Download Gong for Linux amd64:

```sh
curl --fail --location --output gong_linux_amd64.tar.gz \
  https://github.com/creatorofuniverses/gong/releases/latest/download/gong_linux_amd64.tar.gz
tar -xzf gong_linux_amd64.tar.gz
```

The first release is still pending. For now, run `just build` and use
`bin/gong` wherever this page shows `./gong`. See [Installation](docs/installation.md)
for macOS, ARM64, checksums, and authenticated downloads.

Create a bot with [@BotFather](https://t.me/BotFather). The short
[bot setup guide](docs/bot.md) shows what to click. Then
[find your chat ID](docs/chat-id.md) for a private chat or group. Feel free to use
your preferred method, or skip this step if you already have the ID.

Copy the example and add your bot token and chat ID:

```sh
test -e gong.yaml || cp gong.example.yaml gong.yaml
# Edit telegram.bot_token and targets.default.chat_id.
chmod 0600 gong.yaml
```

In your first terminal, start Gong. It finds `./gong.yaml` automatically:

```sh
./gong serve
```

In a second terminal, send your first event:

```sh
curl --fail-with-body -H 'Content-Type: text/plain' \
  --data-binary "🔔 It's gong!" http://localhost:8080/notify
```

If `🔔 It's gong!` appears in Telegram, you're ready. The CLI uses the same config:

```sh
./gong notify -- '✅ Backup finished'
```

Changed `listen`? Use that port in curl; the CLI follows the config.

Want to dress things up? Messages support emoji and [Telegram HTML formatting](docs/usage.md#format-your-messages)
by default: bold text, links, code blocks, and more.

## Guides

Where next?

- Get started: [bot](docs/bot.md), [chat ID](docs/chat-id.md), [installation](docs/installation.md)
- Make it your own: [configuration](docs/configuration.md), [running in the background and Docker](docs/background.md)
- Send events: [curl, CLI, and shell](docs/usage.md), [Python](docs/python.md)
- Organize messages: [forum topics](docs/topics.md)
- Look under the hood: [architecture](docs/architecture.md), [recipe index](docs/recipes.md)

[MIT License](LICENSE)
