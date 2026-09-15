# Use Gong from Python

Start with a function you can paste into a script. When you need task progress,
forum topics, or structured errors, use the ready-made client below.

## Contents

- [Copy a notification function](#copy-a-notification-function)
- [Use the ready-made HTTP client](#use-the-ready-made-http-client)
- [Report a task](#report-a-task)
- [Organize a project with task presets](#organize-a-project-with-task-presets)

## Copy a notification function

With Gong running and [installed on your PATH](installation.md#put-gong-on-your-path),
copy this into your Python file. It uses only the standard library:

```python
import html
import subprocess


def notify(message):
    subprocess.run(
        ["gong", "notify", "--", html.escape(str(message))],
        check=True,
    )


notify("Backup finished")
```

Call `notify(...)` wherever you need a message. This function sends literal text,
so values such as `rows < 100` are safe. The CLI finds your
[YAML config](configuration.md#config-discovery) and reads `GONG_URL` and
`GONG_API_TOKEN`. It prints the JSON response and raises `CalledProcessError`
if Gong returns a nonzero exit status; a missing binary raises `FileNotFoundError`.
The call waits for delivery, with the CLI's default 30-second timeout.

If a notification is optional, handle that at the call site:

```python
try:
    notify("Import finished")
except (subprocess.CalledProcessError, FileNotFoundError):
    print("Could not confirm the notification; check Gong and Telegram.")
```

Do not retry blindly: the message may already have arrived. For automatic task
reporting that keeps notification failures from interrupting your work, see
[Report a task](#report-a-task).

## Use the ready-made HTTP client

Want to send directly over HTTP without installing the Gong CLI on the client
machine? Copy [examples/python/gong.py](../examples/python/gong.py) next to your
script. It uses only the standard library and is not a published package:

```text
my-project/
├── gong.py       # copied from examples/python/gong.py
└── job.py        # your script
```

In `job.py`:

```python
from gong import Gong

client = Gong(timeout=30.0)
client.notify(
    "<b>Backup</b> finished",
    topic="backup",
    level="success",
    category="result",
)
```

Run `python3 job.py`. The client reads `GONG_URL` and `GONG_API_TOKEN`, and defaults
to `http://localhost:8080`. It does not read Gong's YAML config. If your server
uses another port or an API token, set those variables or pass them directly:

```python
client = Gong(url="http://localhost:8081", token="YOUR_API_TOKEN", timeout=30.0)
```

## Report a task

The task context sends a start and then a result or exception. Notification
trouble never replaces the task's original exception. Names, results, and
errors are HTML-escaped for you.

```python
from gong import Gong

client = Gong()
with client.task("Import <clients>", topic="result") as task:
    rows = 42
    task.result = f"{rows} rows"
```

For text passed directly to `notify`, escape dynamic values with `html.escape`.

## Organize a project with task presets

For recurring backup or training jobs, copy both `gong.py` and
[recipes.py](../examples/python/recipes.py) beside your own script:

```text
my-project/
├── gong.py
├── recipes.py
└── job.py
```

`backup` and `training` in
[examples/python/recipes.py](../examples/python/recipes.py) are convenience
wrappers over the same API:

```python
from gong import Gong
from recipes import backup

with backup(Gong(), "Nightly backup") as task:
    task.result = "ok"
```

`GongError` exposes `status`, `code`, `uncertain`, and `retry_after`. The client
does not follow redirects or retry mutations. Check Telegram before retrying an
uncertain call. A failed pin is reported separately after successful delivery.
