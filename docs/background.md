# Keep Gong running

For a quick foreground run, start in the directory containing `gong.yaml`:

```sh
./gong serve
```

Use `--config /path/to/gong.yaml` to choose another file. Gong writes JSON logs
to stderr.

## nohup

```sh
install -d -m 0700 .run
nohup ./gong serve --config "$(pwd)/gong.yaml" \
  >>"$(pwd)/.run/gong.log" 2>&1 &
printf '%s\n' "$!" > .run/gong.pid
curl --fail --silent --show-error http://localhost:8080/health
```

Stop the saved process cleanly:

```sh
pid=$(cat .run/gong.pid)
kill -TERM "$pid"
while kill -0 "$pid" 2>/dev/null; do sleep 1; done
rm -f .run/gong.pid
```

Gong gives active requests up to 30 seconds to finish. Process managers should
allow at least 35 seconds before SIGKILL.

## systemd

The included [service unit](../deploy/gong.service) runs Gong as its own user
and uses `TimeoutStopSec=35s`:

```sh
sudo useradd --system --user-group --no-create-home --home-dir /nonexistent \
  --shell /usr/sbin/nologin gong
sudo install -m 0755 ./gong /usr/local/bin/gong
sudo install -d -o root -g gong -m 0750 /etc/gong
sudo install -o root -g gong -m 0640 gong.yaml /etc/gong/gong.yaml
sudo install -m 0644 deploy/gong.service /etc/systemd/system/gong.service
sudo systemctl daemon-reload
sudo systemctl enable --now gong.service
```

The config install command replaces `/etc/gong/gong.yaml`; back up an existing
one first. Check the service with:

```sh
systemctl status gong.service
journalctl -u gong.service -f
```

Restart the service after replacing the binary or config.

## Docker Compose

Compose builds from a source checkout. The container must listen on
`0.0.0.0:8080`:

```sh
test -e gong.yaml || cp gong.example.yaml gong.yaml
# Set bot_token, chat_id, and listen: "0.0.0.0:8080".
sudo chgrp 10001 gong.yaml
chmod 0640 gong.yaml
docker compose up --build -d
curl --fail --silent --show-error http://localhost:8080/health
docker compose logs -f gong
```

`GONG_PORT` changes the published host port. The container stays on port 8080:

```sh
GONG_PORT=8081 docker compose up --build -d
curl --fail --silent --show-error http://localhost:8081/health
GONG_URL=http://localhost:8081 ./gong notify -- 'Docker is ready'
```

Keep `GONG_PORT=8081` on later Compose commands. The host-side CLI needs
`GONG_URL` because its discovered config contains the container's internal
address. Compose mounts the config read-only, runs as `10001:10001`, publishes
on host loopback, and allows 35 seconds to stop. The healthcheck tests Gong,
not Telegram. The mount uses an SELinux `Z` label where needed.

Apply config changes with `docker compose down`, then `docker compose up -d`.

## Another container or a remote client

Set `api_token` whenever clients use a container name, external DNS name, or
reverse proxy:

```yaml
listen: "0.0.0.0:8080"
api_token: "A_LONG_RANDOM_TOKEN"
```

From the same Docker network:

```sh
export GONG_URL='http://gong:8080'
export GONG_API_TOKEN='A_LONG_RANDOM_TOKEN'
gong notify -- 'Hello from another container'
```

For internet access, put Gong behind a TLS reverse proxy, forward the
`Authorization: Bearer ...` header, and keep Gong's port off the public
network. Use a trusted certificate and leave TLS verification enabled.
Host checking without `api_token` is not authentication.
