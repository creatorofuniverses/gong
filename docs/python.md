# Use Gong from Python

[examples/python/gong.py](../examples/python/gong.py) is a small standard-library
client you can copy into your project. It is not a published package.

```sh
export PYTHONPATH="$PWD/examples/python"
python3 - <<'PY'
from gong import Gong

client = Gong(timeout=30.0)
client.notify(
    "<b>Backup</b> finished",
    topic="backup",
    level="success",
    category="result",
)
PY
```

The client reads `GONG_URL` and `GONG_API_TOKEN`, and defaults to
`http://localhost:8080`. It does not read Gong's YAML config. You can also pass
the connection directly:

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
