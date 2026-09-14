"""Small standard-library client and task helper for a Gong gateway."""

from __future__ import annotations

from contextlib import contextmanager
from dataclasses import dataclass
import html
import http.client
import json
import os
import sys
import time
from typing import Any, Iterator
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlencode
from urllib.request import HTTPRedirectHandler, Request, build_opener


class GongError(Exception):
    """A safe, structured gateway or transport failure."""

    def __init__(
        self,
        error: str,
        *,
        status: int | None = None,
        code: str | None = None,
        uncertain: bool = False,
        retry_after: int | None = None,
    ):
        super().__init__(error)
        self.error = error
        self.status = status
        self.code = code
        self.uncertain = uncertain
        self.retry_after = retry_after


class _NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


@dataclass
class Task:
    """Mutable value yielded by :meth:`Gong.task`."""

    result: Any = None


class Gong:
    """Synchronous JSON client for the Gong HTTP API.

    ``url`` and ``token`` default to ``GONG_URL`` and
    ``GONG_API_TOKEN``. Mutation requests are attempted exactly once and
    redirects are returned as errors.
    """

    def __init__(self, url: str | None = None, token: str | None = None, timeout: float = 30.0):
        self.url = (url or os.environ.get("GONG_URL") or "http://localhost:8080").rstrip("/")
        self.token = token if token is not None else os.environ.get("GONG_API_TOKEN", "")
        self.timeout = timeout
        self._opener = build_opener(_NoRedirects())

    def notify(
        self,
        message: str,
        *,
        target: str = "default",
        topic: str | None = None,
        topic_id: int | None = None,
        level: str = "info",
        category: str | None = None,
        fallback_plain_text: bool = True,
    ) -> dict[str, Any]:
        if topic is not None and topic_id is not None:
            raise ValueError("topic and topic_id are mutually exclusive")
        payload: dict[str, Any] = {
            "message": message,
            "target": target,
            "level": level,
            "fallback_plain_text": fallback_plain_text,
        }
        if topic is not None:
            payload["topic"] = topic
        if topic_id is not None:
            payload["topic_id"] = topic_id
        if category is not None:
            payload["category"] = category
        response = self._request("POST", "/notify", payload)
        if response.get("ok") is True and response.get("pin_status") == "failed":
            print(
                "Gong: message delivered, but pinning failed; do not retry the notification.",
                file=sys.stderr,
            )
        return response

    def create_topic(self, name: str, *, target: str = "default") -> dict[str, Any]:
        return self._request("POST", "/topics", {"name": name, "target": target})

    def delete_topic(self, topic_id: int, *, target: str = "default") -> dict[str, Any]:
        path = f"/topics/{quote(str(topic_id), safe='')}?{urlencode({'target': target})}"
        return self._request("DELETE", path, None)

    @contextmanager
    def task(
        self,
        name: str,
        *,
        target: str = "default",
        topic: str | None = None,
        topic_id: int | None = None,
    ) -> Iterator[Task]:
        """Report task start and outcome without changing task semantics."""

        common = {"target": target, "topic": topic, "topic_id": topic_id}
        safe_name = html.escape(str(name), quote=True)
        self._best_effort_notify(
            f"<b>{safe_name}</b>\nStarted", level="info", category="start", **common
        )
        started = time.monotonic()
        state = Task()
        try:
            yield state
        except BaseException as exc:
            try:
                elapsed = time.monotonic() - started
                safe_type = html.escape(type(exc).__name__, quote=True)
                safe_error = html.escape(str(exc), quote=True)
                self._best_effort_notify(
                    f"<b>{safe_name}</b>\nFailed\nElapsed: {elapsed:.3f}s\nError: {safe_type}: {safe_error}",
                    level="error",
                    category="error",
                    **common,
                )
            except BaseException:
                # Failure reporting is cleanup; an active task exception wins.
                pass
            raise
        else:
            elapsed = time.monotonic() - started
            safe_result = html.escape(str(state.result), quote=True)
            self._best_effort_notify(
                f"<b>{safe_name}</b>\nCompleted\nElapsed: {elapsed:.3f}s\nResult: {safe_result}",
                level="success",
                category="result",
                **common,
            )

    def _best_effort_notify(self, message: str, **kwargs: Any) -> None:
        try:
            self.notify(message, **kwargs)
        except Exception as exc:
            if isinstance(exc, GongError):
                detail = f"HTTP {exc.status}" if exc.status is not None else "transport error"
                if exc.code:
                    detail += f" ({exc.code})"
                if exc.uncertain:
                    detail += "; outcome uncertain; do not retry blindly"
            else:
                detail = type(exc).__name__
            print(f"Gong notification failed: {detail}", file=sys.stderr)

    def _request(self, method: str, path: str, payload: dict[str, Any] | None) -> dict[str, Any]:
        body = None
        headers = {"Accept": "application/json"}
        if payload is not None:
            body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if self.token:
            headers["Authorization"] = f"Bearer {self.token}"
        request = Request(self.url + path, data=body, headers=headers, method=method)
        try:
            with self._opener.open(request, timeout=self.timeout) as response:
                return self._decode_response(response.read(), response.status)
        except HTTPError as exc:
            try:
                response_body = exc.read()
            except Exception:
                try:
                    exc.close()
                except Exception:
                    pass
                raise self._uncertain_error(
                    "Gong response could not be read", status=exc.code
                ) from None
            try:
                exc.close()
            except Exception:
                pass
            try:
                parsed = json.loads(response_body)
            except (UnicodeDecodeError, json.JSONDecodeError):
                raise self._uncertain_error(
                    f"Gong returned HTTP {exc.code} with invalid JSON", status=exc.code
                ) from None
            if 300 <= exc.code < 400 or not self._is_gateway_error(parsed):
                raise self._uncertain_error(
                    f"Gong returned HTTP {exc.code} without a structured error", status=exc.code
                ) from None
            uncertain = parsed.get("uncertain", False)
            retry_after = parsed.get("retry_after")
            raise GongError(
                parsed["error"],
                status=exc.code,
                code=parsed["code"],
                uncertain=uncertain,
                retry_after=retry_after,
            ) from None
        except (URLError, TimeoutError, OSError, http.client.HTTPException):
            raise self._uncertain_error(
                "Gong request failed without a trustworthy response"
            ) from None

    @staticmethod
    def _decode_response(body: bytes, status: int) -> dict[str, Any]:
        try:
            value = json.loads(body)
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise Gong._uncertain_error(
                "Gong returned an invalid JSON response", status=status
            ) from None
        if not isinstance(value, dict) or value.get("ok") is not True:
            raise Gong._uncertain_error(
                "Gong returned an invalid success response", status=status
            ) from None
        return value

    @staticmethod
    def _is_gateway_error(value: Any) -> bool:
        if not isinstance(value, dict) or value.get("ok") is not False:
            return False
        if not isinstance(value.get("code"), str) or not value["code"]:
            return False
        if not isinstance(value.get("error"), str) or not value["error"]:
            return False
        if "uncertain" in value and not isinstance(value["uncertain"], bool):
            return False
        retry_after = value.get("retry_after")
        return retry_after is None or (
            isinstance(retry_after, int)
            and not isinstance(retry_after, bool)
            and retry_after > 0
        )

    @staticmethod
    def _uncertain_error(message: str, *, status: int | None = None) -> GongError:
        return GongError(
            f"{message}; mutation outcome is uncertain; do not retry blindly",
            status=status,
            uncertain=True,
        )
