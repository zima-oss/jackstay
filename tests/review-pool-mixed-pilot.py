"""Exercise one local review requested by installed Codex and Claude Code sessions.

Usage: python3 tests/review-pool-mixed-pilot.py /absolute/path/to/jst
Both models and the reviewer use local fixtures. The two sessions use linked Git worktrees.
"""

from contextlib import ExitStack
import hashlib
import http.server
import json
import os
from pathlib import Path
import runpy
import secrets
import shlex
import shutil
import signal
import subprocess
import sys
import tempfile
import threading

codex_pilot = runpy.run_path(str(Path(__file__).with_name("review-pool-codex-pilot.py")))
claude_pilot = runpy.run_path(str(Path(__file__).with_name("review-pool-claude-pilot.py")))
chat = codex_pilot["chat"]
wait_for = chat["wait_for"]
start_process = chat["start_process"]
tool = codex_pilot["tool"]
result = codex_pilot["result"]


class CodexScript(chat["Script"]):
    def __init__(self, state_file, base, head):
        super().__init__(state_file)
        self.base = base
        self.head = head
        self.step = 0
        self.job_id = ""
        self.pending = threading.Event()
        self.finished = threading.Event()

    def codex_message(self):
        snapshot = self.snapshot()
        recipient = next(session["id"] for session in snapshot["sessions"] if session["name"] == "codex")
        return next((message for message in (snapshot.get("messages") or ()) if message["to"] == recipient), None)

    def next_action(self, _role, body):
        self.step += 1
        if self.step == 1:
            return self.step, tool(body, "review_submit"), {
                "base": self.base, "head": self.head, "task": "shared-pool-pilot", "reviewers": ["codex"],
            }
        if self.step == 2:
            submitted = result(body, 1, "job")
            assert submitted["created"] and submitted["job"]["status"] == "pending", submitted
            self.job_id = submitted["job"]["id"]
            return self.step, tool(body, "review_status"), {"job_id": self.job_id}
        if self.step == 3:
            assert result(body, 2, "job")["job"]["status"] == "pending"
            self.pending.set()
            if not self.unblock.wait(55):
                raise RuntimeError("Codex bridge recovery did not complete")
            return self.step, tool(body, "chat_self"), {}
        if self.step == 4:
            message = self.codex_message()
            assert message and message["from"] == "review-pool" and self.job_id in message["body"]
            prompt = json.dumps(body["input"])
            assert "Jackstay session chat delivery" in prompt and message["id"] in prompt
            return self.step, tool(body, "chat_ack"), {"message_id": message["id"]}
        if self.step == 5:
            assert result(body, 4, "acknowledged")["acknowledged"]
            assert self.codex_message()["acknowledged"]
            self.finished.set()
            return self.step, None, None
        raise RuntimeError(f"unexpected Codex step {self.step}")


class ClaudeGateway(claude_pilot["Gateway"]):
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
        names = [entry["name"] for entry in body.get("tools", ())]
        inputs = json.dumps(body["messages"])
        try:
            if step == 1:
                suffix, arguments = "review_submit", {
                    "base": pilot.base, "head": pilot.head, "task": "shared-pool-pilot", "reviewers": ["codex"],
                }
            elif step == 2:
                assert '"created":false' in inputs or '\\"created\\":false' in inputs, inputs[-1200:]
                jobs = pilot.jobs()
                assert len(jobs) == 1 and jobs[0]["id"] == pilot.job_id, jobs
                assert jobs[0]["status"] == "pending", jobs
                pilot.submitted.set()
                suffix, arguments = "review_status", {"job_id": pilot.job_id}
            elif step == 3:
                assert pilot.job_id in inputs and "pending" in inputs
                if not pilot.worker_done.wait(55):
                    raise RuntimeError("local worker did not finish")
                suffix, arguments = "review_status", {"job_id": pilot.job_id}
            elif step == 4:
                assert pilot.job_id in inputs and "complete" in inputs, inputs[-1200:]
                suffix, arguments = "chat_self", {}
            elif step == 5:
                assert "pending Jackstay session chat message" in inputs, "Claude hook did not announce result: " + inputs[-1800:]
                suffix, arguments = "chat_inbox", {}
            elif step == 6:
                assert "review-pool" in inputs and pilot.job_id in inputs
                message = pilot.message("claude")
                assert message and not message.get("acknowledged")
                pilot.claude_message_id = message["id"]
                suffix, arguments = "chat_ack", {"message_id": message["id"]}
            elif step == 7:
                assert "acknowledged" in claude_pilot["latest_tool_result"](body["messages"])
                suffix, arguments = "chat_inbox", {}
            elif step == 8:
                final_inbox = claude_pilot["latest_tool_result"](body["messages"])
                assert "messages" in final_inbox and pilot.claude_message_id not in final_inbox
                suffix, arguments = None, None
            else:
                raise AssertionError(f"unexpected Claude model step {step}")
            tool_name = next((name for name in names if name.replace(".", "_").endswith(suffix)), None) if suffix else None
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
        self.peer = root / "peer"
        self.home = root / "jackstay"
        self.requests = []
        self.errors = []
        self.submitted = threading.Event()
        self.worker_done = threading.Event()
        self.job_id = ""
        self.claude_message_id = ""

    def jobs(self):
        output = subprocess.check_output([self.binary, "review", "pool", "list", "--json"],
                                         cwd=self.repo, env=self.environment)
        return json.loads(output)

    def message(self, name):
        snapshot = json.loads(self.chat_state.read_text())
        recipient = next(session["id"] for session in snapshot["sessions"] if session["name"] == name)
        return next((item for item in snapshot["messages"] if item["to"] == recipient and item["from"] == "review-pool"), None)

    def prepare(self):
        self.repo.mkdir()
        self.environment = {"PATH": os.environ["PATH"], "HOME": str(self.root),
                            "JACKSTAY_HOME": str(self.home), "RUST_LOG": "error"}
        self.base, self.head = codex_pilot["fixture"](self.root, self.repo, self.environment)
        config_path = self.repo / "jackstay.json"
        configuration = json.loads(config_path.read_text())
        configuration["chat"]["claude_code"] = {"enabled": True}
        config_path.write_text(json.dumps(configuration))
        subprocess.run(["git", "add", "jackstay.json"], cwd=self.repo, env=self.environment, check=True)
        subprocess.run(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
                        "-c", "commit.gpgsign=false", "commit", "-qm", "Enable the Claude Code chat hook"],
                       cwd=self.repo, env=self.environment, check=True)
        self.base = self.head
        self.head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=self.repo,
                                            env=self.environment, text=True).strip()
        subprocess.run(["git", "worktree", "add", "-q", "-b", "peer", str(self.peer)],
                       cwd=self.repo, env=self.environment, check=True)
        namespace = hashlib.sha256(str((self.repo / ".git").resolve()).encode()).hexdigest()
        self.chat_state = self.home / ".all" / "session-chat" / namespace / "state.json"

    def run(self, stack):
        self.prepare()
        codex_model = stack.enter_context(http.server.ThreadingHTTPServer(("127.0.0.1", 0), chat["PilotModel"]))
        script = CodexScript(self.chat_state, self.base, self.head)
        codex_model.script = script
        threading.Thread(target=codex_model.serve_forever, daemon=True).start()
        stack.callback(codex_model.shutdown)
        stack.callback(script.unblock.set)

        codex_home = self.root / "codex"
        codex_home.mkdir()
        socket_path = self.root / "codex.sock"
        codex_token = secrets.token_hex(24)
        tools = ["review.submit", "review.status", "chat.self", "chat.ack"]
        (codex_home / "config.toml").write_text(
            'model = "pilot"\nmodel_provider = "local_pilot"\ncheck_for_update_on_startup = false\n'
            '[model_providers.local_pilot]\nname = "Pilot"\n'
            f'base_url = "http://127.0.0.1:{codex_model.server_port}/codex/v1"\nwire_api = "responses"\n'
            '[mcp_servers.jackstay_chat]\n'
            f'command = {json.dumps(self.binary)}\nargs = ["chat", "serve", "--name", "codex"]\n'
            f'cwd = {json.dumps(str(self.repo))}\n'
            'env_vars = ["JACKSTAY_HOME", "JACKSTAY_CHAT_SESSION_TOKEN"]\nrequired = true\n'
            f'enabled_tools = {json.dumps(tools)}\ndefault_tools_approval_mode = "approve"\n'
        )
        codex_env = dict(self.environment, CODEX_HOME=str(codex_home), JACKSTAY_CHAT_SESSION_TOKEN=codex_token)
        codex = start_process(stack, ["codex", "app-server", "--listen", "unix://" + str(socket_path)],
                              self.repo, codex_env, self.root / "codex.log")
        wait_for("Codex app-server", lambda: socket_path.exists() or codex.poll() is not None)
        app = chat["AppServer"](socket_path, stack)
        thread, turn = app.start(self.repo, "Submit the shared review and acknowledge its result.")
        wait_for("Codex pending review", lambda: script.pending.is_set() or script.error)
        script.snapshot()
        self.job_id = script.job_id

        bridge_command = [self.binary, "chat", "codex-bridge", "--socket", str(socket_path),
                          "--thread", thread, "--name", "codex"]
        bridge_log = self.root / "bridge.log"
        bridge = start_process(stack, bridge_command, self.repo, codex_env, bridge_log)

        claude_gateway = stack.enter_context(http.server.ThreadingHTTPServer(("127.0.0.1", 0), ClaudeGateway))
        claude_gateway.pilot = self
        threading.Thread(target=claude_gateway.serve_forever, daemon=True).start()
        stack.callback(claude_gateway.shutdown)
        claude_env = dict(self.environment, CLAUDE_CONFIG_DIR=str(self.root / "claude"),
                          JACKSTAY_CHAT_SESSION_TOKEN=secrets.token_hex(24),
                          ANTHROPIC_AUTH_TOKEN="local-synthetic-fixture",
                          ANTHROPIC_BASE_URL=f"http://127.0.0.1:{claude_gateway.server_port}",
                          DISABLE_TELEMETRY="1", DISABLE_NONESSENTIAL_TRAFFIC="1",
                          DISABLE_AUTOUPDATER="1", ENABLE_TOOL_SEARCH="false")
        hook = shlex.join([self.binary, "chat", "claude-hook", "--name", "claude", "--event", "PostToolUse"])
        settings = json.dumps({"hooks": {"PostToolUse": [{"matcher": ".*", "hooks": [{"type": "command",
                                                                                         "command": hook, "timeout": 5}]}]}})
        mcp = json.dumps({"mcpServers": {"jackstay_chat": {"command": self.binary,
                                                            "args": ["chat", "serve", "--name", "claude"]}}})
        claude = shutil.which("claude")
        if claude is None:
            raise RuntimeError("installed Claude Code is required")
        claude_process = start_process(stack,
            [claude, "-p", "--model", "sonnet", "--setting-sources", "", "--settings", settings,
             "--mcp-config", mcp, "--strict-mcp-config", "--tools", "mcp__jackstay_chat",
             "--allowedTools", "mcp__jackstay_chat", "--no-session-persistence", "--",
             "Join the shared review, read the terminal notification and acknowledge it."],
            self.peer, claude_env, self.root / "claude.log")
        claude_process.stdin.close()
        wait_for("Claude joined one queued review", lambda: self.submitted.is_set() or self.errors, timeout=35)
        assert not self.errors, self.errors
        assert len(self.jobs()) == 1

        with (self.root / "worker.log").open("w") as log:
            worker = subprocess.run([self.binary, "review", "pool", "next"], cwd=self.repo, env=self.environment,
                                    stdout=log, stderr=subprocess.STDOUT, timeout=35)
            assert worker.returncode == 0, (worker.returncode, log.name)
        self.worker_done.set()

        accepted = wait_for("Codex active-turn acceptance", lambda: (script.codex_message() or {}).get("codex_accepted"))
        assert accepted["turn_id"] == turn and accepted["thread_id"] == thread
        os.killpg(bridge.pid, signal.SIGKILL)
        bridge.wait(timeout=3)
        codex_message = script.codex_message()
        assert not codex_message.get("acknowledged")
        start_process(stack, bridge_command, self.repo, codex_env, bridge_log)
        wait_for("bridge resumed pending ACK", lambda: "still pending recipient acknowledgment" in bridge_log.read_text(), timeout=20)

        script.unblock.set()
        wait_for("Codex MCP ACK", lambda: script.finished.is_set() or script.error, timeout=35)
        assert not script.error, script.error
        wait_for("Claude MCP ACK", lambda: claude_process.poll() is not None or self.errors, timeout=55)
        assert not self.errors and claude_process.returncode == 0 and len(self.requests) == 8, (
            self.errors, claude_process.returncode, len(self.requests))
        snapshot = json.loads(self.chat_state.read_text())
        messages = [message for message in snapshot["messages"] if message["from"] == "review-pool"]
        assert len(messages) == 2 and {message["to"] for message in messages} == {
            session["id"] for session in snapshot["sessions"] if session["name"] in ("codex", "claude")}
        assert all(message.get("acknowledged") for message in messages)
        assert bridge_log.read_text().count("Codex accepted 1 message(s)") == 1
        print("PASS: linked-worktree Codex and Claude Code sessions deduplicated one review, received two notices and ACKed after bridge restart")


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="jst-pool-mixed-") as directory:
        location = Path(directory)
        try:
            with ExitStack() as lifecycle:
                Pilot(location, Path(sys.argv[1]).resolve(strict=True)).run(lifecycle)
        except Exception:
            for logfile in location.glob("*.log"):
                print(logfile.name, logfile.read_text()[-3000:], file=sys.stderr)
            state_file = location / "jackstay" / ".all" / "session-chat"
            for snapshot in state_file.glob("*/state.json"):
                print("chat-state", snapshot.read_text()[-1800:], file=sys.stderr)
            raise
