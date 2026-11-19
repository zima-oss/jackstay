"""Exercise the reviewer pool from a native Codex turn against a loopback model.

Usage: python3 tests/review-pool-codex-pilot.py /absolute/path/to/jst
"""

from contextlib import ExitStack
import hashlib
import http.server
import json
import os
from pathlib import Path
import runpy
import secrets
import subprocess
import sys
import tempfile
import threading


chat = runpy.run_path(str(Path(__file__).with_name("codex-chat-pilot.py")))
wait_for = chat["wait_for"]
start_process = chat["start_process"]


def tool(body, suffix):
    for entry in body["tools"]:
        if entry["type"] == "namespace":
            for candidate in entry["tools"]:
                if candidate["name"].replace(".", "_").endswith(suffix):
                    return entry["name"], candidate["name"]
        if entry["type"] == "function" and entry["name"].replace(".", "_").endswith(suffix):
            return "", entry["name"]
    raise RuntimeError(f"Codex did not discover {suffix}")


def result(body, step, field):
    output = next(item["output"] for item in body["input"]
                  if item.get("type") == "function_call_output" and item["call_id"] == f"call_codex_{step}")
    parts = [item.get("text", "") for item in output] if isinstance(output, list) else [output]
    for part in "\n".join(parts).splitlines():
        if part.startswith("{"):
            parsed = json.loads(part)
            if field in parsed:
                return parsed
    raise RuntimeError(f"MCP output {step} lacks {field}: {output!r}")


class ReviewScript(chat["Script"]):
    def __init__(self, state_file, base, head):
        super().__init__(state_file)
        self.base = base
        self.head = head
        self.step = 0
        self.job = None
        self.pending = threading.Event()
        self.finished = threading.Event()

    def next_action(self, _role, body):
        self.step += 1
        if self.step == 1:
            return self.step, tool(body, "review_submit"), {
                "base": self.base, "head": self.head, "task": "native-codex-pilot", "reviewers": ["codex"],
            }
        if self.step == 2:
            submitted = result(body, 1, "job")
            assert submitted["created"] and submitted["job"]["base"] == self.base
            assert submitted["job"]["head"] == self.head
            self.job = submitted["job"]["id"]
            return self.step, tool(body, "review_status"), {"job_id": self.job}
        if self.step == 3:
            assert result(body, 2, "job")["job"]["status"] == "pending"
            self.pending.set()
            if not self.unblock.wait(45):
                raise RuntimeError("review completion was not accepted into the active turn")
            return self.step, tool(body, "chat_self"), {}
        if self.step == 4:
            message = self.message()
            prompt = json.dumps(body["input"])
            assert "Jackstay session chat delivery" in prompt and message["id"] in prompt
            assert message["from"] == "review-pool" and self.job in message["body"]
            return self.step, tool(body, "review_status"), {"job_id": self.job}
        if self.step == 5:
            job = result(body, 4, "job")["job"]
            assert job["status"] == "complete" and job["exit"] == 0 and job["attempts"] == 1, job
            return self.step, tool(body, "chat_ack"), {"message_id": self.message()["id"]}
        if self.step == 6:
            assert result(body, 5, "acknowledged")["acknowledged"]
            assert self.message()["acknowledged"]
            self.finished.set()
            return self.step, None, None
        raise RuntimeError(f"unexpected Codex model call {self.step}")


def fixture(root, checkout, environment):
    reviewer = root / "synthetic-reviewer"
    reviewer.write_text("""#!/bin/sh
set -eu
if [ "$1" = "--version" ]; then
    printf 'codex-cli 9.9.9\\n'
    exit 0
fi
cat >/dev/null
while [ "$1" != "-o" ]; do shift; done
printf '%s\\n' '{"verdict":"approve","summary":"Synthetic review passed","findings":[],"prior":[]}' > "$2"
printf '%s\\n' '{"type":"turn.completed"}'
""")
    reviewer.chmod(0o700)

    def git(*args):
        return subprocess.check_output(["git", *args], cwd=checkout, env=environment, text=True).strip()

    git("init", "-q", "-b", "main")
    git("config", "user.name", "Jackstay pilot")
    git("config", "user.email", "pilot@example.invalid")
    (checkout / "jackstay.json").write_text(json.dumps({
        "jackstay_schema": 2,
        "project": "review-pilot",
        "chat": {"enabled": True, "codex_bridge": {"enabled": True, "poll_interval_ms": 100}},
        "review": {"reviewers": {"codex": {"command": str(reviewer)}}},
    }))
    git("add", "jackstay.json")
    git("commit", "-qm", "Configure synthetic review")
    base = git("rev-parse", "HEAD")
    (checkout / "sample.txt").write_text("synthetic change\n")
    git("add", "sample.txt")
    git("commit", "-qm", "Add synthetic change")
    return base, git("rev-parse", "HEAD")


def run(binary, root, stack):
    checkout = root / "repo"
    checkout.mkdir()
    home = root / "jackstay"
    environment = {"PATH": os.environ["PATH"], "HOME": str(root), "JACKSTAY_HOME": str(home),
                   "RUST_LOG": "error", "PWD": str(checkout)}
    base, head = fixture(root, checkout, environment)
    namespace = hashlib.sha256(str((checkout / ".git").resolve()).encode()).hexdigest()
    script = ReviewScript(home / ".all" / "session-chat" / namespace / "state.json", base, head)
    model = stack.enter_context(http.server.ThreadingHTTPServer(("127.0.0.1", 0), chat["PilotModel"]))
    model.script = script
    threading.Thread(target=model.serve_forever, daemon=True).start()
    stack.callback(model.shutdown)
    stack.callback(script.unblock.set)

    codex_home = root / "codex"
    codex_home.mkdir()
    socket_path = root / "codex.sock"
    token = secrets.token_hex(24)
    tools = ["review.submit", "review.status", "chat.self", "chat.ack"]
    (codex_home / "config.toml").write_text(
        'model = "pilot"\nmodel_provider = "local_pilot"\ncheck_for_update_on_startup = false\n'
        '[model_providers.local_pilot]\nname = "Pilot"\n'
        f'base_url = "http://127.0.0.1:{model.server_port}/codex/v1"\nwire_api = "responses"\n'
        '[mcp_servers.jackstay_chat]\n'
        f'command = {json.dumps(str(binary))}\nargs = ["chat", "serve", "--name", "codex"]\n'
        f'cwd = {json.dumps(str(checkout))}\n'
        'env_vars = ["JACKSTAY_HOME", "JACKSTAY_CHAT_SESSION_TOKEN"]\nrequired = true\n'
        f'enabled_tools = {json.dumps(tools)}\ndefault_tools_approval_mode = "approve"\n'
    )
    session_env = dict(environment, CODEX_HOME=str(codex_home), JACKSTAY_CHAT_SESSION_TOKEN=token)
    host = start_process(stack, ["codex", "app-server", "--listen", "unix://" + str(socket_path)],
                         checkout, session_env, root / "codex.log")
    wait_for("Codex server", lambda: socket_path.exists() or host.poll() is not None)
    app = chat["AppServer"](socket_path, stack)
    thread, turn = app.start(checkout, "Submit a Jackstay review, track it, and acknowledge completion.")
    wait_for("pending MCP status", lambda: script.pending.is_set() or script.error)
    script.snapshot()

    bridge = [str(binary), "chat", "codex-bridge", "--socket", str(socket_path), "--thread", thread, "--name", "codex"]
    start_process(stack, bridge, checkout, session_env, root / "bridge.log")
    with (root / "worker.log").open("w") as log:
        subprocess.run([str(binary), "review", "pool", "next"], cwd=checkout, env=environment,
                       stdout=log, stderr=subprocess.STDOUT, check=True, timeout=30)

    accepted = wait_for("review completion accepted by active turn",
                        lambda: (script.message() or {}).get("codex_accepted"))
    assert accepted["turn_id"] == turn and accepted["thread_id"] == thread
    assert not script.message().get("acknowledged")
    script.unblock.set()
    wait_for("terminal status and MCP ACK", lambda: script.finished.is_set() or script.error)
    messages = script.snapshot()["messages"]
    assert len(messages) == 1 and messages[0]["acknowledged"]
    print("PASS: native Codex MCP submit, pending status, local worker, active-turn notification and ACK")


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="jrp-", dir="/tmp") as directory:
        location = Path(directory)
        try:
            with ExitStack() as lifecycle:
                run(Path(sys.argv[1]).resolve(strict=True), location, lifecycle)
        except Exception:
            for logfile in location.glob("*.log"):
                print(logfile.name, logfile.read_text()[-3000:], file=sys.stderr)
            raise
