# Keep Gong running

## Contents

- [Choose a launch method](#choose-a-launch-method)
- [Try a foreground run](#try-a-foreground-run)
- [nohup](#nohup)
- [systemd](#systemd)
  - [Run as your Linux user](#run-as-your-linux-user)
  - [Run as a system service](#run-as-a-system-service)
- [macOS: launchd](#macos-launchd)
- [Docker Compose](#docker-compose)
- [Another container or a remote client](#another-container-or-a-remote-client)

## Choose a launch method

| Method | Use it for | What to expect |
|---|---|---|
| `nohup` (Linux/macOS) | A quick background run | Survives terminal hangups; no automatic restart or start at boot. |
| `systemd --user` (Linux with systemd) | Everyday use under your own account | Starts at login, restarts on failure, collects logs; no root service needed. |
| System systemd service (Linux) | A shared server | Starts at boot under a dedicated account; setup needs administrator access. |
| `launchd` user agent (macOS) | Everyday use on a Mac | Built in; starts at login and restarts the process without sudo. |
| Docker Compose | An existing container setup | Requires Docker Engine and Compose, or Docker Desktop on macOS. |

For personal use, start with `systemd --user` on Linux or `launchd` on macOS.
macOS does not ship with systemd or Docker. Choose one method at a time: stop
an existing Gong process before starting another on the same port. None of
these methods can keep Gong serving requests while the machine is asleep.

## Try a foreground run

For a quick foreground run, start in the directory containing `gong.yaml`:

```sh
./gong serve
```

Use `--config /path/to/gong.yaml` to choose another file. Gong writes JSON logs
to stderr.

## nohup

`nohup` makes a command ignore the hangup signal normally sent when its terminal
closes. The trailing `&` puts it in the background; `nohup` alone does not.
It is useful for a quick session on Linux or macOS with no service setup.
See the [nohup manual](https://www.gnu.org/software/coreutils/manual/html_node/nohup-invocation.html).

It does not restart Gong after a crash or reboot, rotate logs, or provide a
service status command. System logout policies can still terminate user
processes. For an unattended service, use a process manager below.

Run from the directory containing the binary and `gong.yaml`:

```sh
install -d -m 0700 .run
nohup ./gong serve --config "$(pwd)/gong.yaml" \
  < /dev/null >>"$(pwd)/.run/gong.log" 2>&1 &
printf '%s\n' "$!" > .run/gong.pid
```

Check startup and follow the log (use your configured port):

```sh
curl --fail --silent --show-error http://localhost:8080/health
```

```sh
tail -f .run/gong.log
```

Stop the saved process cleanly from the same directory. If Gong has already
exited, verify the PID with `ps -p "$(cat .run/gong.pid)"` before sending a signal:
PIDs can be reused by another process.

```sh
pid=$(cat .run/gong.pid)
kill -TERM "$pid"
while kill -0 "$pid" 2>/dev/null; do sleep 1; done
rm -f .run/gong.pid
```

Gong gives active requests up to 30 seconds to finish. Process managers should
allow at least 35 seconds before SIGKILL.

## systemd

### Run as your Linux user

On a Linux system with systemd, a user service is enough for a personal Gong
instance. First [install the binary](installation.md#put-gong-on-your-path) at
`~/.local/bin/gong` and [prepare your config](configuration.md#keep-a-config-in-your-home-directory)
at `~/.config/gong/config.yaml`.

Create the service directory:

```sh
mkdir -p "$HOME/.config/systemd/user"
```

Save this as `~/.config/systemd/user/gong.service`:

```ini
[Unit]
Description=Gong Telegram notification gateway

[Service]
ExecStart="%h/.local/bin/gong" serve --config "%h/.config/gong/config.yaml"
Restart=on-failure
RestartSec=5s
TimeoutStopSec=35s

[Install]
WantedBy=default.target
```

`%h` expands to your home directory. If your config is under `XDG_CONFIG_HOME`
or elsewhere, replace the `--config` argument with its absolute path.
The service does not read your shell startup files; put Gong settings in its
config rather than relying on terminal exports.

Start now and automatically at future logins:

```sh
systemctl --user daemon-reload
systemctl --user enable --now gong.service
systemctl --user status gong.service
journalctl --user -u gong.service -f
```

After editing the config or replacing the binary, restart with
`systemctl --user restart gong.service`. After editing the unit, run
`systemctl --user daemon-reload` first. To stop and disable it:

```sh
systemctl --user disable --now gong.service
```

To start the user manager at boot and keep it running after logout, enable
*lingering*:

```sh
loginctl enable-linger "$USER"
loginctl show-user "$USER" -p Linger
```

This may need administrator authorization depending on your distribution's
policy. Normal user service commands do not need sudo. See
[systemd user units](https://www.freedesktop.org/software/systemd/man/latest/systemd.unit.html)
and [loginctl lingering](https://www.freedesktop.org/software/systemd/man/latest/loginctl.html).

### Run as a system service

Use this on a shared server when Gong should have its own account and a
system-wide config. Setup requires sudo.

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

## macOS: launchd

macOS includes `launchd`, its native process manager. A user LaunchAgent runs
under your account, starts at login, and can restart Gong when it exits. It
stops when you log out. See [Apple's LaunchAgent guide](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html).

First [install the binary](installation.md#put-gong-on-your-path) at
`~/.local/bin/gong` and [prepare your config](configuration.md#keep-a-config-in-your-home-directory).
Create the agent and log directories:

```sh
mkdir -p "$HOME/Library/LaunchAgents" "$HOME/Library/Logs/Gong"
```

Save this as `~/Library/LaunchAgents/local.gong.plist`. Replace **every**
`/Users/YOUR_NAME` with your actual home directory (run `echo "$HOME"` to see it).
Use the actual config path if you set `XDG_CONFIG_HOME`. Plist paths must be
absolute: `~` and `$HOME` are not expanded. Escape `&` as `&amp;` in XML paths.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>local.gong</string>
    <key>ProgramArguments</key>
    <array>
        <string>/Users/YOUR_NAME/.local/bin/gong</string>
        <string>serve</string>
        <string>--config</string>
        <string>/Users/YOUR_NAME/.config/gong/config.yaml</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ExitTimeOut</key>
    <integer>35</integer>
    <key>StandardOutPath</key>
    <string>/Users/YOUR_NAME/Library/Logs/Gong/gong.log</string>
    <key>StandardErrorPath</key>
    <string>/Users/YOUR_NAME/Library/Logs/Gong/gong.log</string>
</dict>
</plist>
```

From Terminal in your logged-in Mac desktop session, validate and start it:

```sh
plutil -lint "$HOME/Library/LaunchAgents/local.gong.plist"
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/local.gong.plist"
launchctl print "gui/$(id -u)/local.gong"
tail -f "$HOME/Library/Logs/Gong/gong.log"
```

Check `curl --fail http://localhost:8080/health` using your configured port.
The agent reads Gong's config, not exports from your shell startup files.
`KeepAlive` restarts the process after it exits; logs append to the file, so
periodically archive or clear it.

To stop, or before applying config, binary, or plist changes:

```sh
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/local.gong.plist"
```

Run the `bootstrap` command again after making changes. To remove automatic
startup at login, move the plist out of `~/Library/LaunchAgents` after stopping
it. For local command details, run `man launchctl` and `man launchd.plist`.

## Docker Compose

Use this if you already have Docker, or specifically want a container. Install
Docker Engine with the Compose plugin on Linux, or Docker Desktop on macOS,
first; Docker is not included with macOS. For a Mac without containers, use
[launchd](#macos-launchd).

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
not Telegram. The mount requests an SELinux `Z` label where needed.

### SELinux: container cannot read the config

On SELinux systems such as Fedora, some Compose versions ignore `selinux: Z`
when `create_host_path: false` is set. This was reproduced with Compose 5.5.0;
see the [Compose issue](https://github.com/docker/compose/issues/13396).
Gong then exits with `config_failed`, even when the file's Unix permissions
are correct.

If SELinux is enforcing (`getenforce` prints `Enforcing`), label this config
file for container access and recreate the container:

```sh
docker compose down
sudo chcon -t container_file_t -l s0 gong.yaml
docker compose up -d --wait
```

Keep the `0640` permissions and group `10001` from the setup above. This labels
only `gong.yaml`; it does not disable SELinux. Reapply the label if you replace
the file or restore its default SELinux context. Keep any `GONG_PORT` override
on these commands too.

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
