# Configure Gong

Start with the example, without replacing a config you already have:

```sh
test -e gong.yaml || cp gong.example.yaml gong.yaml
chmod 0600 gong.yaml
```

Set `telegram.bot_token` and `targets.default.chat_id`. The server needs both.

## Keep a config in your home directory

For Gong to work from any directory, keep your config at
`$HOME/.config/gong/config.yaml` (`~/.config/gong/config.yaml`) on Linux or
macOS. If you set an absolute `XDG_CONFIG_HOME`, use
`$XDG_CONFIG_HOME/gong/config.yaml` instead.

From the extracted release or source checkout, copy the example once:

```sh
case "${XDG_CONFIG_HOME:-}" in
  /*) config_dir="$XDG_CONFIG_HOME/gong" ;;
  *) config_dir="$HOME/.config/gong" ;;
esac
install -d -m 0700 "$config_dir"
if [ ! -e "$config_dir/config.yaml" ]; then
  install -m 0600 gong.example.yaml "$config_dir/config.yaml"
fi
```

Edit `"$config_dir/config.yaml"` and set the bot token and chat ID. With
[Gong on your PATH](installation.md#put-gong-on-your-path), run `gong serve`
or `gong notify -- 'Hello'` from any directory. A local `./gong.yaml` takes
priority over this shared config; `--config` selects a file explicitly.

## Optional API token

`api_token` is a shared secret you choose to protect access to Gong's HTTP API.
It is separate from the Telegram bot token: clients use it to authenticate to
Gong, while Gong uses `telegram.bot_token` to send messages through Telegram.

For local use at `localhost`, it is optional: leave `api_token: ""` or omit the
field. Set a long random secret when clients connect from another machine,
through a reverse proxy, or by a container name. Give those clients the same
secret using `GONG_API_TOKEN`, the CLI's `--token` flag, or their config's
`api_token` field. HTTP clients send it as `Authorization: Bearer YOUR_API_TOKEN`.
`GONG_API_TOKEN` is the environment-variable form of this setting; it is also
optional and can override the server's YAML value.

## Config discovery

Without `--config`, `serve`, `notify`, and `topics` look in this order:

1. `./gong.yaml`
2. `${XDG_CONFIG_HOME}/gong/config.yaml` when `XDG_CONFIG_HOME` is absolute
3. `$HOME/.config/gong/config.yaml` when XDG is unset, empty, or relative

The first file found wins. A malformed discovered file is an error. With
`--config FILE`, Gong uses that exact path and reports a missing or invalid file.

Clients can run without a config: they use `--url`, `GONG_URL`, or
`http://localhost:8080`. When a client does read the server config, it takes
`listen` and `api_token` from it. Wildcard listen hosts such as `0.0.0.0` become
loopback for a local client. Clients only validate `listen` and `api_token`;
they do not need a bot token or chat ID. This is enough for a client:

```yaml
listen: "127.0.0.1:8081"
# Optional: only if the server has an API token configured.
# api_token: "YOUR_API_TOKEN"
```

Config-derived addresses stay local: loopback, wildcard, or `localhost`.
For a remote gateway, choose the destination explicitly with `--url` or
`GONG_URL`. Invalid YAML and unknown fields are still errors.

Client URL and token precedence is:

1. `--url` / `--token`
2. nonempty `GONG_URL` / `GONG_API_TOKEN`
3. discovered or explicit config
4. localhost:8080 / no token

If you change the port, restart the server. A client in the same directory
follows the config automatically:

```yaml
listen: "127.0.0.1:8081"
```

```sh
./gong serve
./gong notify -- 'Uses port 8081'
```

From elsewhere, choose the config or endpoint:

```sh
gong notify --config /path/to/gong.yaml -- 'Uses that file'
GONG_URL=http://localhost:8081 gong notify -- 'Uses the environment'
gong notify --url http://localhost:8081 -- 'Uses the flag'
```

## Full server config

```yaml
listen: "127.0.0.1:8080"
telegram:
  bot_token: "YOUR_BOT_TOKEN"
  proxy_url: ""
  timeout: "10s"
targets:
  default:
    chat_id: "YOUR_CHAT_ID"
    mode: "chat"
  operations:
    chat_id: "YOUR_FORUM_CHAT_ID"
    mode: "forum"
notify_min_level: "warning"
pin_categories: []
api_token: ""
max_topics: 10000
```

| Field | What it does |
|---|---|
| `listen` | HTTP address; default `127.0.0.1:8080`. |
| `telegram.bot_token` | Required unless nonempty `GONG_BOT_TOKEN` overrides it. |
| `telegram.proxy_url` | Direct when empty; otherwise `http`, `https`, `socks5`, or `socks5h`, with no path/query/fragment. |
| `telegram.timeout` | Limit for one Telegram call; default `10s`. |
| `targets` | Chat aliases. `default` is required; names allow letters, digits, `_`, and `-`. |
| `targets.*.chat_id` | Nonzero decimal Telegram ID, kept in quotes. |
| `targets.*.mode` | `chat` (default) or `forum`. |
| `notify_min_level` | Sound threshold. Order: `debug`, `info`, `success`, `warning`, `error`; lower levels still arrive silently. |
| `pin_categories` | Exact category names to pin; default `[]`. |
| `api_token` | Optional shared secret for Gong API access; default empty. Set it for container-name or remote access. See [API token](#optional-api-token). |
| `max_topics` | In-memory automatic topic-name limit; default `10000`. |

Unknown fields and multiple YAML documents are rejected. Nonempty
`GONG_BOT_TOKEN`, `GONG_PROXY_URL`, and `GONG_API_TOKEN` override YAML. Empty
variables do not erase file values. Restart the server after editing its config.

## Sound and pinning

`silent: true` in a response means Gong asked Telegram to deliver the message
without sound. Telegram can still show a notification or banner; this setting
does not hide it ([Telegram reference](https://core.telegram.org/bots/api#sendmessage)).
Gong calculates `silent` from the message's `level` and `notify_min_level`;
it is a response field, not a request option. The default threshold is `warning`.

Categories are names you choose. This pins results and errors:

```yaml
notify_min_level: "warning"
pin_categories: ["result", "error"]
```

```sh
./gong notify --level success --category result -- 'Job finished'
```

The message arrives silently because `success` is below `warning`, and Gong
tries to pin it. Pinning is always silent. If delivery succeeds but pinning
fails, the response keeps `ok: true` and reports `pin_status: "failed"`.

## Telegram proxy

```yaml
telegram:
  bot_token: "YOUR_BOT_TOKEN"
  proxy_url: "socks5h://user:password@proxy.example.com:1080"
  timeout: "10s"
```

Supported schemes are `http`, `https`, `socks5`, and `socks5h`. TLS
verification stays on. If the configured proxy fails, Gong does not connect
directly instead. `HTTP_PROXY` and `HTTPS_PROXY` are not read implicitly. This
proxy is for Gong → Telegram, not CLI → Gong.
