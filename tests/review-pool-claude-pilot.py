"""Exercise the installed Claude Code CLI against the review pool MCP tools.

Usage: python3 tests/review-pool-claude-pilot.py /absolute/path/to/jst
The model endpoint and reviewer are local fixtures; no account credentials are used.
"""

import hashlib
import http.server
import json
import os
from pathlib import Path
import secrets
import shlex
import shutil
import subprocess
import sys
import tempfile
import threading


def latest_tool_result(messages):
    for message in reversed(messages):
        for item in message.get("content", ()) if isinstance(message.get("content"), list) else ():
            if isinstance(item, dict) and item.get("type") == "tool_result":
                return json.dumps(item)
    raise AssertionError("Claude did not return an MCP tool result")


class Gateway(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"data":[],"has_more":false}')

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if "messages" not in body:
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"input_tokens":1}')
            return
        pilot = self.server.pilot
        step = len(pilot.requests) + 1
        pilot.requests.append(body)
        names = [tool["name"] for tool in body.get("tools", ())]
        inputs = json.dumps(body["messages"])
        try:
            if step == 1:
                suffix, arguments = "review_submit", {
                    "base": pilot.base, "head": pilot.head, "task": "claude-pool-pilot", "reviewers": ["claude-code"],
                }
            elif step == 2:
                assert "claude-pool-pilot" in inputs, f"submit result missing: {inputs[-1000:]}"
                jobs = json.loads(subprocess.run(
                    [pilot.binary, "review", "pool", "list", "--json"], cwd=pilot.repo,
                    env=pilot.environment, text=True, capture_output=True, check=True, timeout=10,
                ).stdout)
                assert len(jobs) == 1 and jobs[0]["status"] == "pending", jobs
                pilot.job_id = jobs[0]["id"]
                suffix, arguments = "review_status", {"job_id": pilot.job_id}
            elif step == 3:
                assert pilot.job_id in inputs and "pending" in inputs, "pending status missing"
                worker = subprocess.run(
                    [pilot.binary, "review", "pool", "next"], cwd=pilot.repo,
                    env=pilot.environment, text=True, capture_output=True, timeout=35,
                )
                assert worker.returncode == 0, (worker.returncode, worker.stdout[-500:], worker.stderr[-500:])
                pilot.worker_output = worker.stdout
                suffix, arguments = "review_status", {"job_id": pilot.job_id}
            elif step == 4:
                assert pilot.job_id in inputs and "complete" in inputs, f"terminal status missing: {inputs[-1200:]}"
                assert "pending Jackstay session chat message" in inputs, "Claude hook did not notify at tool boundary"
                suffix, arguments = "chat_inbox", {}
            elif step == 5:
                assert "review-pool" in inputs and pilot.job_id in inputs, "terminal inbox event missing"
                messages = json.loads(pilot.chat_state.read_text())["messages"]
                pending = [message for message in messages if message.get("from") == "review-pool" and not message.get("acknowledged")]
                assert len(pending) == 1, pending
                pilot.message_id = pending[0]["id"]
                suffix, arguments = "chat_ack", {"message_id": pilot.message_id}
            elif step == 6:
                assert "acknowledged" in latest_tool_result(body["messages"]), "ACK result missing"
                suffix, arguments = "chat_inbox", {}
            elif step == 7:
                final_inbox = latest_tool_result(body["messages"])
                assert "messages" in final_inbox and pilot.message_id not in final_inbox, "ACKed event repeated"
                suffix, arguments = None, None
            else:
                raise AssertionError(f"unexpected model step {step}")
            if suffix:
                tool_name = next(name for name in names if name.replace(".", "_").endswith(suffix))
            else:
                tool_name = None
        except Exception as failure:
            pilot.errors.append(f"step {step}: {failure}")
            tool_name, arguments = None, None
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        content = {"type": "tool_use", "id": f"toolu_fixture_{step}", "name": tool_name, "input": {}} if tool_name else {"type": "text", "text": ""}
        delta = {"type": "input_json_delta", "partial_json": json.dumps(arguments)} if tool_name else {"type": "text_delta", "text": "pilot complete"}
        events = [
            ("message_start", {"message": {"id": f"msg_fixture_{step}", "type": "message", "role": "assistant",
                                        "model": body["model"], "stop_reason": None, "stop_sequence": None,
                                        "usage": {"input_tokens": 1, "output_tokens": 1}, "content": []}}),
            ("content_block_start", {"index": 0, "content_block": content}),
            ("content_block_delta", {"index": 0, "delta": delta}),
            ("content_block_stop", {"index": 0}),
            ("message_delta", {"delta": {"stop_reason": "tool_use" if tool_name else "end_turn",
                                           "stop_sequence": None}, "usage": {"output_tokens": 1}}),
            ("message_stop", {}),
        ]
        for event, data in events:
            self.wfile.write((f"event: {event}\ndata: " + json.dumps({"type": event, **data}) + "\n\n").encode())
        self.wfile.flush()


class Pilot:
    def __init__(self, root, binary):
        self.root = root
        self.binary = str(binary)
        self.repo = root / "repo"
        self.repo.mkdir()
        self.environment = {
            "PATH": os.environ["PATH"], "HOME": str(root), "CLAUDE_CONFIG_DIR": str(root / "claude"),
            "JACKSTAY_HOME": str(root / "jst"), "JACKSTAY_CHAT_SESSION_TOKEN": secrets.token_hex(24),
            "ANTHROPIC_AUTH_TOKEN": "local-synthetic-fixture", "DISABLE_TELEMETRY": "1",
            "DISABLE_NONESSENTIAL_TRAFFIC": "1", "DISABLE_AUTOUPDATER": "1", "ENABLE_TOOL_SEARCH": "false",
        }
        self.requests = []
        self.errors = []
        self.job_id = None
        self.message_id = None
        self.worker_output = None

    def git(self, *arguments):
        return subprocess.run(["git", *arguments], cwd=self.repo, env=self.environment, text=True, capture_output=True,
                              check=True, timeout=10).stdout.strip()

    def prepare(self):
        self.git("init", "-q", "-b", "main")
        reviewer = self.root / "synthetic-claude"
        reviewer.write_text("#!/bin/sh\nset -eu\n"
                            "if [ \"${1:-}\" = --version ]; then echo '2.1.248 (Claude Code)'; exit 0; fi\n"
                            "cat >/dev/null\n"
                            "printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,"
                            "\"structured_output\":{\"verdict\":\"approve\",\"summary\":\"synthetic local review\","
                            "\"findings\":[],\"prior\":[]}}'\n")
        reviewer.chmod(0o700)
        config = {"jackstay_schema": 3, "project": "claude-pool-pilot",
                  "chat": {"enabled": True, "claude_code": {"enabled": True}},
                  "review": {"reviewers": {"claude-code": {"command": str(reviewer)}}}}
        (self.repo / "jackstay.json").write_text(json.dumps(config))
        (self.repo / "reviewed.txt").write_text("before\n")
        self.git("add", ".")
        self.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Fixture base")
        self.base = self.git("rev-parse", "HEAD")
        (self.repo / "reviewed.txt").write_text("after\n")
        self.git("add", "reviewed.txt")
        self.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                 "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Fixture head")
        self.head = self.git("rev-parse", "HEAD")
        scope = hashlib.sha256(str((self.repo / ".git").resolve()).encode()).hexdigest()
        self.chat_state = self.root / "jst" / ".all" / "session-chat" / scope / "state.json"

    def run(self):
        self.prepare()
        claude = shutil.which("claude")
        if claude is None:
            raise RuntimeError("installed Claude Code is required on PATH")
        with http.server.ThreadingHTTPServer(("127.0.0.1", 0), Gateway) as gateway:
            gateway.pilot = self
            thread = threading.Thread(target=gateway.serve_forever, daemon=True)
            thread.start()
            self.environment["ANTHROPIC_BASE_URL"] = f"http://127.0.0.1:{gateway.server_port}"
            hook = shlex.join([self.binary, "chat", "claude-hook", "--name", "pilot", "--event", "PostToolUse"])
            settings = json.dumps({"hooks": {"PostToolUse": [{"matcher": ".*", "hooks": [{"type": "command",
                                                                                             "command": hook, "timeout": 5}]}]}})
            mcp = json.dumps({"mcpServers": {"jackstay_chat": {"command": self.binary,
                                                                "args": ["chat", "serve", "--name", "pilot"]}}})
            try:
                result = subprocess.run(
                    [claude, "-p", "--model", "sonnet", "--setting-sources", "", "--settings", settings,
                     "--mcp-config", mcp, "--strict-mcp-config", "--tools", "mcp__jackstay_chat",
                     "--allowedTools", "mcp__jackstay_chat", "--no-session-persistence", "--",
                     "Submit the local synthetic review, check status, then process and ACK the completion notice."],
                    cwd=self.repo, env=self.environment, capture_output=True, text=True, timeout=60,
                )
            finally:
                gateway.shutdown()
        if result.returncode or self.errors or len(self.requests) != 7:
            raise RuntimeError(f"Claude exit={result.returncode}; steps={len(self.requests)}; errors={self.errors}; "
                               f"stdout={result.stdout[-500:]}; stderr={result.stderr[-500:]}")
        messages = json.loads(self.chat_state.read_text())["messages"]
        assert any(message["id"] == self.message_id and message.get("acknowledged") for message in messages)
        print("PASS: installed Claude Code submitted and polled review via MCP; local worker completed; "
              "PostToolUse delivered terminal event; inbox ACK removed pending event")


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="jst-pool-claude-") as directory:
        Pilot(Path(directory), Path(sys.argv[1]).resolve(strict=True)).run()
