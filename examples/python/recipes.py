"""Ready-to-copy task presets built on :mod:`gong`."""

from gong import Gong


def backup(client: Gong, name: str = "Backup", *, target: str = "default"):
    """Return a task context that reports in the ``backup`` topic."""

    return client.task(name, target=target, topic="backup")


def training(client: Gong, name: str = "Training", *, target: str = "default"):
    """Return a task context that reports in the ``training`` topic."""

    return client.task(name, target=target, topic="training")
