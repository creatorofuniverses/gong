import contextlib
import http.client
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import traceback
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.error import HTTPError, URLError


sys.path.insert(0, str(Path(__file__).parent))

from gong import Gong, GongError
from recipes import backup, training


class RecordingHandler(BaseHTTPRequestHandler):
    requests = []
    response_status = 200
    response_body = {"ok": True, "message_id": 17}
    response_raw = None

    def do_POST(self):
        self._handle()

    def do_DELETE(self):
        self._handle()

    def _handle(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        type(self).requests.append(
            {
                "method": self.command,
                "path": self.path,
                "authorization": self.headers.get("Authorization"),
                "content_type": self.headers.get("Content-Type"),
                "body": json.loads(body) if body else None,
            }
        )
        encoded = type(self).response_raw
        if encoded is None:
            encoded = json.dumps(type(self).response_body).encode()
        self.send_response(type(self).response_status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)

    def log_message(self, _format, *_args):
        pass


class MockGateway:
    def __enter__(self):
        RecordingHandler.requests = []
        RecordingHandler.response_status = 200
        RecordingHandler.response_body = {"ok": True, "message_id": 17}
        RecordingHandler.response_raw = None
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), RecordingHandler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        host, port = self.server.server_address
        self.url = f"http://{host}:{port}"
        return self

    def __exit__(self, *_args):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()


class GongTests(unittest.TestCase):
    def test_defaults_to_timeout_longer_than_gateway_deadline(self):
        self.assertEqual(Gong().timeout, 30.0)

    def test_json_api_sends_unicode_auth_and_both_topic_forms(self):
        with MockGateway() as gateway:
            client = Gong(gateway.url + "/", token="secret", timeout=1)
            client.notify(
                "готово\nстрока 2",
                target="ops",
                topic="обучение",
                level="success",
                category="результат",
                fallback_plain_text=False,
            )
            client.notify("by id", topic_id=9223372036854775807)
            RecordingHandler.response_status = 201
            client.create_topic("ручной", target="ops")
            RecordingHandler.response_status = 200
            client.delete_topic(41, target="ops")

        requests = RecordingHandler.requests
        self.assertEqual([item["method"] for item in requests], ["POST", "POST", "POST", "DELETE"])
        self.assertEqual([item["path"] for item in requests], ["/notify", "/notify", "/topics", "/topics/41?target=ops"])
        self.assertTrue(all(item["authorization"] == "Bearer secret" for item in requests))
        self.assertTrue(all(item["content_type"] == "application/json" for item in requests[:3]))
        self.assertEqual(
            requests[0]["body"],
            {
                "message": "готово\nстрока 2",
                "target": "ops",
                "topic": "обучение",
                "level": "success",
                "category": "результат",
                "fallback_plain_text": False,
            },
        )
        self.assertEqual(requests[1]["body"]["topic_id"], 9223372036854775807)
        self.assertEqual(requests[2]["body"], {"name": "ручной", "target": "ops"})
        self.assertIsNone(requests[3]["body"])

    def test_redirect_is_returned_as_error_without_replaying_mutation(self):
        with MockGateway() as gateway:
            RecordingHandler.response_status = 307
            RecordingHandler.response_body = {"code": "redirect"}
            client = Gong(gateway.url, token="secret", timeout=1)
            with self.assertRaises(GongError) as raised:
                client.notify("once")

        self.assertEqual(raised.exception.status, 307)
        self.assertIs(raised.exception.uncertain, True)
        self.assertIn("do not retry blindly", str(raised.exception))
        self.assertEqual(len(RecordingHandler.requests), 1)

    def test_error_preserves_gateway_outcome_fields(self):
        with MockGateway() as gateway:
            RecordingHandler.response_status = 504
            RecordingHandler.response_body = {
                "ok": False,
                "code": "request_timeout",
                "error": "delivery outcome is unknown",
                "uncertain": True,
                "retry_after": 9,
            }
            with self.assertRaises(GongError) as raised:
                Gong(gateway.url).notify("one attempt")

        self.assertEqual(str(raised.exception), "delivery outcome is unknown")
        self.assertEqual(raised.exception.status, 504)
        self.assertEqual(raised.exception.code, "request_timeout")
        self.assertIs(raised.exception.uncertain, True)
        self.assertEqual(raised.exception.retry_after, 9)
        self.assertEqual(len(RecordingHandler.requests), 1)

    def test_structured_gateway_error_can_report_certain_rejection(self):
        with MockGateway() as gateway:
            RecordingHandler.response_status = 422
            RecordingHandler.response_body = {
                "ok": False,
                "code": "invalid_request",
                "error": "topic_id must be positive",
                "uncertain": False,
            }
            with self.assertRaises(GongError) as raised:
                Gong(gateway.url).delete_topic(0)

        self.assertIs(raised.exception.uncertain, False)
        self.assertNotIn("do not retry blindly", str(raised.exception))

    def test_transport_timeout_is_uncertain_and_warns_against_retry(self):
        class TimingOutOpener:
            def open(self, _request, timeout):
                raise TimeoutError("request may have been written")

        client = Gong("http://localhost:8080")
        client._opener = TimingOutOpener()
        with self.assertRaises(GongError) as raised:
            client.create_topic("one attempt")

        self.assertIs(raised.exception.uncertain, True)
        self.assertIn("do not retry blindly", str(raised.exception))

    def test_incomplete_success_response_is_closed_and_uncertain(self):
        class IncompleteResponse:
            status = 200
            closed = False

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                self.closed = True

            def read(self):
                raise http.client.IncompleteRead(b'{"ok":', 7)

        response = IncompleteResponse()

        class IncompleteOpener:
            def open(self, _request, timeout):
                return response

        client = Gong("http://localhost:8080")
        client._opener = IncompleteOpener()
        with self.assertRaises(GongError) as raised:
            client.notify("may be delivered")

        self.assertIs(response.closed, True)
        self.assertIs(raised.exception.uncertain, True)
        self.assertIn("do not retry blindly", str(raised.exception))

    def test_invalid_or_non_object_success_response_is_uncertain(self):
        for raw in (b'{"ok":', b"[]"):
            with self.subTest(raw=raw), MockGateway() as gateway:
                RecordingHandler.response_raw = raw
                with self.assertRaises(GongError) as raised:
                    Gong(gateway.url).notify("may be delivered")

            self.assertIs(raised.exception.uncertain, True)
            self.assertIn("do not retry blindly", str(raised.exception))

    def test_http_error_body_read_failure_is_safe_and_uncertain(self):
        class BrokenBody:
            def read(self):
                raise OSError("secret.invalid/private")

            def close(self):
                pass

        class BrokenErrorOpener:
            def open(self, _request, timeout):
                raise HTTPError(
                    "http://user:token@secret.invalid/private",
                    502,
                    "bad gateway",
                    {},
                    BrokenBody(),
                )

        client = Gong("http://user:token@secret.invalid", token="bearer-secret")
        client._opener = BrokenErrorOpener()
        with self.assertRaises(GongError) as raised:
            client.notify("may be delivered")

        formatted = "".join(traceback.format_exception(raised.exception))
        self.assertIs(raised.exception.uncertain, True)
        self.assertIn("do not retry blindly", formatted)
        self.assertNotIn("secret.invalid", formatted)
        self.assertNotIn("bearer-secret", formatted)
        self.assertNotIn("user:token", formatted)

    def test_malformed_http_error_is_uncertain(self):
        with MockGateway() as gateway:
            RecordingHandler.response_status = 502
            RecordingHandler.response_raw = b'{"ok":false'
            with self.assertRaises(GongError) as raised:
                Gong(gateway.url).delete_topic(41)

        self.assertIs(raised.exception.uncertain, True)
        self.assertIn("do not retry blindly", str(raised.exception))

    def test_partial_pin_failure_returns_success_and_warns_against_retry(self):
        stderr = io.StringIO()
        with MockGateway() as gateway:
            RecordingHandler.response_body = {
                "ok": True,
                "message_id": 17,
                "pin_status": "failed",
                "pin_error": "already delivered; do not retry",
            }
            with contextlib.redirect_stderr(stderr):
                response = Gong(gateway.url).notify("one attempt", category="result")

        self.assertTrue(response["ok"])
        self.assertEqual(response["pin_status"], "failed")
        self.assertIn("delivered", stderr.getvalue())
        self.assertIn("do not retry", stderr.getvalue())
        self.assertEqual(len(RecordingHandler.requests), 1)

    def test_transport_traceback_does_not_contain_request_url_or_token(self):
        class FailingOpener:
            def open(self, _request, timeout):
                raise URLError("http://user:token@secret.invalid/private")

        client = Gong("http://user:token@secret.invalid", token="bearer-secret")
        client._opener = FailingOpener()
        try:
            client.notify("message")
        except GongError as exc:
            formatted = "".join(traceback.format_exception(exc))
        else:
            self.fail("GongError was not raised")
        self.assertNotIn("secret.invalid", formatted)
        self.assertNotIn("bearer-secret", formatted)
        self.assertNotIn("user:token", formatted)

    def test_task_reports_escaped_start_result_and_elapsed(self):
        with MockGateway() as gateway:
            client = Gong(gateway.url, timeout=1)
            with client.task("train <nightly>", topic="training") as task:
                task.result = "accuracy 99% & rising"

        messages = [request["body"]["message"] for request in RecordingHandler.requests]
        self.assertEqual(len(messages), 2)
        self.assertIn("train &lt;nightly&gt;", messages[0])
        self.assertNotIn("<nightly>", messages[0])
        self.assertIn("accuracy 99% &amp; rising", messages[1])
        self.assertRegex(messages[1], r"Elapsed: \d+\.\d{3}s")
        self.assertEqual(RecordingHandler.requests[0]["body"]["category"], "start")
        self.assertEqual(RecordingHandler.requests[1]["body"]["category"], "result")

    def test_notification_failure_never_masks_original_exception(self):
        original = RuntimeError("task <secret>")
        stderr = io.StringIO()
        with MockGateway() as gateway:
            RecordingHandler.response_status = 500
            RecordingHandler.response_body = {"code": "upstream", "message": "safe failure"}
            client = Gong(gateway.url, token="do-not-print", timeout=1)
            with contextlib.redirect_stderr(stderr):
                with self.assertRaises(RuntimeError) as raised:
                    with client.task("job <unsafe>"):
                        raise original

        self.assertIs(raised.exception, original)
        self.assertEqual(len(RecordingHandler.requests), 2)
        self.assertIn("job &lt;unsafe&gt;", RecordingHandler.requests[1]["body"]["message"])
        self.assertIn("task &lt;secret&gt;", RecordingHandler.requests[1]["body"]["message"])
        self.assertNotIn("do-not-print", stderr.getvalue())
        self.assertNotIn(gateway.url, stderr.getvalue())
        self.assertIn("outcome uncertain; do not retry blindly", stderr.getvalue())

    def test_failure_reporting_cannot_replace_active_task_exception(self):
        class UnrenderableError(RuntimeError):
            def __str__(self):
                raise KeyboardInterrupt("render interrupted")

        original = UnrenderableError()
        with MockGateway() as gateway:
            client = Gong(gateway.url)
            with self.assertRaises(UnrenderableError) as raised:
                with client.task("fragile cleanup"):
                    raise original

        self.assertIs(raised.exception, original)
        self.assertEqual(len(RecordingHandler.requests), 1)

    def test_recipe_presets_supply_topics_and_return_task_result(self):
        with MockGateway() as gateway:
            client = Gong(gateway.url, timeout=1)
            with backup(client, "database") as task:
                task.result = "archive.tar"
            with training(client, "classifier") as task:
                task.result = {"accuracy": 0.98}

        bodies = [request["body"] for request in RecordingHandler.requests]
        self.assertEqual([body["topic"] for body in bodies], ["backup", "backup", "training", "training"])
        self.assertEqual([body["category"] for body in bodies], ["start", "result", "start", "result"])
        self.assertIn("archive.tar", bodies[1]["message"])
        self.assertIn("&#x27;accuracy&#x27;", bodies[3]["message"])


class ShellRecipeTests(unittest.TestCase):
    def setUp(self):
        self.temp_dir = tempfile.TemporaryDirectory()
        root = Path(self.temp_dir.name)
        self.log_path = root / "calls.jsonl"
        stub = root / "gong"
        stub.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, sys\n"
            "with open(os.environ['GONG_TEST_LOG'], 'a', encoding='utf-8') as out:\n"
            "    print(json.dumps(['gong', *sys.argv[1:]], ensure_ascii=False), file=out)\n"
            "if os.environ.get('GONG_TEST_NOTIFY_STDOUT'):\n"
            "    print(os.environ['GONG_TEST_NOTIFY_STDOUT'])\n"
            "if os.environ.get('GONG_TEST_NOTIFY_STDERR'):\n"
            "    print(os.environ['GONG_TEST_NOTIFY_STDERR'], file=sys.stderr)\n"
            "raise SystemExit(int(os.environ.get('GONG_TEST_NOTIFY_STATUS', '0')))\n",
            encoding="utf-8",
        )
        stub.chmod(0o755)
        command = root / "record-command"
        command.write_text(
            "#!/usr/bin/env python3\n"
            "import json, os, sys\n"
            "with open(os.environ['GONG_TEST_LOG'], 'a', encoding='utf-8') as out:\n"
            "    print(json.dumps(['command', *sys.argv[1:]], ensure_ascii=False), file=out)\n"
            "raise SystemExit(int(os.environ.get('GONG_TEST_COMMAND_STATUS', '0')))\n",
            encoding="utf-8",
        )
        command.chmod(0o755)
        self.env = os.environ.copy()
        self.env.update(
            {
                "PATH": f"{root}{os.pathsep}{self.env['PATH']}",
                "GONG_TEST_LOG": str(self.log_path),
                "GONG_TEST_NOTIFY_STATUS": "19",
                "GONG_TEST_COMMAND_STATUS": "7",
            }
        )

    def tearDown(self):
        self.temp_dir.cleanup()

    def _calls(self):
        return [json.loads(line) for line in self.log_path.read_text(encoding="utf-8").splitlines()]

    def test_bash_wrapper_preserves_argv_status_and_escapes_message(self):
        script = Path(__file__).parents[1] / "shell" / "gong.bash"
        run = subprocess.run(
            [
                "bash",
                "-c",
                'set -e; source "$1"; gong_backup "db <main> & nightly" record-command "space arg" "*"',
                "bash",
                str(script),
            ],
            env=self.env,
            text=True,
            capture_output=True,
        )
        self.assertEqual(run.returncode, 7)
        calls = self._calls()
        self.assertEqual(calls[0], ["command", "space arg", "*"])
        self.assertEqual(calls[1][0:6], ["gong", "notify", "--topic", "backup", "--level", "error"])
        self.assertEqual(calls[1][6:8], ["--category", "result"])
        self.assertEqual(calls[1][8], "--")
        self.assertIn("db &lt;main&gt; &amp; nightly", calls[1][9])
        self.assertIn("Exit status: 7", calls[1][9])

    def test_bash_notify_preserves_one_multiline_message_argument(self):
        script = Path(__file__).parents[1] / "shell" / "gong.bash"
        self.env["GONG_TEST_NOTIFY_STATUS"] = "0"
        run = subprocess.run(
            [
                "bash",
                "-c",
                'source "$1"; message="line one\nline two with * and spaces"; gong_notify --topic "nightly backup" -- "$message"',
                "bash",
                str(script),
            ],
            env=self.env,
            text=True,
            capture_output=True,
        )
        self.assertEqual(run.returncode, 0)
        self.assertEqual(
            self._calls(),
            [["gong", "notify", "--topic", "nightly backup", "--", "line one\nline two with * and spaces"]],
        )

    def test_bash_success_is_not_replaced_by_notification_failure(self):
        script = Path(__file__).parents[1] / "shell" / "gong.bash"
        self.env["GONG_TEST_COMMAND_STATUS"] = "0"
        run = subprocess.run(
            ["bash", "-c", 'set -e; source "$1"; gong_notify_after "successful task" record-command', "bash", str(script)],
            env=self.env,
            text=True,
            capture_output=True,
        )
        self.assertEqual(run.returncode, 0)
        calls = self._calls()
        self.assertEqual(calls[0], ["command"])
        self.assertIn("Success", calls[1][-1])

    def test_bash_partial_delivery_keeps_cli_diagnostics_and_uses_neutral_wording(self):
        script = Path(__file__).parents[1] / "shell" / "gong.bash"
        self.env.update(
            {
                "GONG_TEST_COMMAND_STATUS": "0",
                "GONG_TEST_NOTIFY_STATUS": "1",
                "GONG_TEST_NOTIFY_STDOUT": '{"ok":true,"pin_status":"failed"}',
                "GONG_TEST_NOTIFY_STDERR": "message delivered; pin failed; do not retry",
            }
        )
        run = subprocess.run(
            ["bash", "-c", 'source "$1"; gong_notify_after "partial" record-command', "bash", str(script)],
            env=self.env,
            text=True,
            capture_output=True,
        )
        self.assertEqual(run.returncode, 0)
        self.assertIn('{"ok":true,"pin_status":"failed"}', run.stdout)
        self.assertIn("message delivered; pin failed; do not retry", run.stderr)
        self.assertIn("Gong returned a nonzero status; command status is unchanged.", run.stderr)
        self.assertNotIn("Gong notification failed", run.stderr)

    @unittest.skipUnless(subprocess.run(["sh", "-c", "command -v fish"], capture_output=True).returncode == 0, "fish unavailable")
    def test_fish_wrapper_preserves_argv_status_and_escapes_message(self):
        script = Path(__file__).parents[1] / "shell" / "gong.fish"
        run = subprocess.run(
            [
                "fish",
                "-c",
                'source "$argv[1]"; gong_training "model <v2> & more" record-command "space arg" "*"',
                str(script),
            ],
            env=self.env,
            text=True,
            capture_output=True,
        )
        self.assertEqual(run.returncode, 7)
        calls = self._calls()
        self.assertEqual(calls[0], ["command", "space arg", "*"])
        self.assertEqual(calls[1][0:6], ["gong", "notify", "--topic", "training", "--level", "error"])
        self.assertEqual(calls[1][6:8], ["--category", "result"])
        self.assertEqual(calls[1][8], "--")
        self.assertEqual(len(calls[1]), 10)
        self.assertIn("model &lt;v2&gt; &amp; more", calls[1][9])
        self.assertIn("\nExit status: 7\nElapsed: ", calls[1][9])

    @unittest.skipUnless(subprocess.run(["sh", "-c", "command -v fish"], capture_output=True).returncode == 0, "fish unavailable")
    def test_fish_partial_delivery_uses_neutral_wording(self):
        script = Path(__file__).parents[1] / "shell" / "gong.fish"
        self.env.update(
            {
                "GONG_TEST_COMMAND_STATUS": "0",
                "GONG_TEST_NOTIFY_STATUS": "1",
                "GONG_TEST_NOTIFY_STDOUT": '{"ok":true,"pin_status":"failed"}',
                "GONG_TEST_NOTIFY_STDERR": "message delivered; pin failed; do not retry",
            }
        )
        run = subprocess.run(
            ["fish", "-c", 'source "$argv[1]"; gong_notify_after "partial" record-command', str(script)],
            env=self.env,
            text=True,
            capture_output=True,
        )
        self.assertEqual(run.returncode, 0)
        self.assertIn('{"ok":true,"pin_status":"failed"}', run.stdout)
        self.assertIn("message delivered; pin failed; do not retry", run.stderr)
        self.assertIn("Gong returned a nonzero status; command status is unchanged.", run.stderr)
        self.assertNotIn("Gong notification failed", run.stderr)


if __name__ == "__main__":
    unittest.main()
