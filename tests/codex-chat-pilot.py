"""Run two installed Codex sessions against a scripted loopback model.

Usage: python3 tests/codex-chat-pilot.py /absolute/path/to/jst
"""

import base64
from contextlib import ExitStack
import hashlib
import http.server
import json
import os
from pathlib import Path
import secrets
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time


def wait_for(description, predicate, timeout=25):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(0.025)
    raise RuntimeError("timed out waiting for " + description)


class AppServer:
    def __init__(self, path, stack):
        self.connection = stack.enter_context(socket.socket(socket.AF_UNIX))
        self.connection.settimeout(15)
        self.connection.connect(str(path))
        self.reader = stack.enter_context(self.connection.makefile("rb"))
        key = base64.b64encode(os.urandom(16))
        self.connection.sendall(
            b"GET / HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\n"
            b"Connection: Upgrade\r\nSec-WebSocket-Version: 13\r\n"
            b"Sec-WebSocket-Key: " + key + b"\r\n\r\n"
        )
        if b" 101 " not in self.reader.readline():
            raise RuntimeError("Codex WebSocket upgrade failed")
        while self.reader.readline() not in (b"\r\n", b""):
            pass
        self.sequence = 0
        self.request("initialize", {
            "clientInfo": {"name": "jackstay-pilot", "version": "1"},
            "capabilities": {"experimentalApi": True},
        })
        self.send({"method": "initialized"})

    def send(self, value):
        content = json.dumps(value).encode()
        length = bytes([0x80 | len(content)]) if len(content) < 126 else b"\xfe" + struct.pack("!H", len(content))
        mask = os.urandom(4)
        self.connection.sendall(b"\x81" + length + mask + bytes(byte ^ mask[index % 4] for index, byte in enumerate(content)))

    def receive(self):
        while True:
            frame, size = self.reader.read(2)
            if size == 126:
                size = struct.unpack("!H", self.reader.read(2))[0]
            elif size == 127:
                size = struct.unpack("!Q", self.reader.read(8))[0]
            body = self.reader.read(size)
            if frame == 0x81:
                return json.loads(body)
            if frame == 0x88:
                raise RuntimeError("Codex closed the pilot socket")

    def request(self, method, params):
        self.sequence += 1
        self.send({"id": self.sequence, "method": method, "params": params})
        while True:
            received = self.receive()
            if received.get("id") == self.sequence:
                if "error" in received:
                    raise RuntimeError(f"{method}: {received['error']}")
                return received["result"]

    def start(self, cwd, label):
        thread = self.request("thread/start", {
            "cwd": str(cwd), "historyMode": "legacy", "approvalPolicy": "never",
        })["thread"]["id"]
        turn = self.request("turn/start", {
            "threadId": thread, "input": [{"type": "text", "text": label}],
        })["turn"]["id"]
        return thread, turn


class PilotModel(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        role = self.path.split("/")[1]
        script = self.server.script
        try:
            step, tool, arguments = script.next_action(role, body)
            item_id = f"item_{role}_{step}"
            if tool:
                namespace, name = tool
                item = {"id": item_id, "type": "function_call", "call_id": f"call_{role}_{step}",
                        "name": name, "arguments": json.dumps(arguments), "status": "completed"}
                if namespace:
                    item["namespace"] = namespace
            else:
                item = {"id": item_id, "type": "message", "role": "assistant", "status": "completed",
                        "content": [{"type": "output_text", "text": "pilot complete", "annotations": []}]}
            response = {"id": f"resp_{role}_{step}", "object": "response", "status": "completed", "output": [item],
                        "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2,
                                  "input_tokens_details": {"cached_tokens": 0},
                                  "output_tokens_details": {"reasoning_tokens": 0}}}
            events = [("response.output_item.done", {"item": item, "output_index": 0}),
                      ("response.completed", {"response": response})]
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for event, data in events:
                self.wfile.write(("data: " + json.dumps({"type": event, **data}) + "\n\n").encode())
            self.wfile.flush()
        except (ValueError, KeyError, RuntimeError, AssertionError) as problem:
            script.error = repr(problem)
            self.send_error(400, "Pilot model assertion failed")


class Script:
    def __init__(self, state_file):
        self.state_file = state_file
        self.steps = {"sender": 0, "recipient": 0}
        self.unblock = threading.Event()
        self.error = None

    def snapshot(self):
        if self.error:
            raise RuntimeError(self.error)
        try:
            return json.loads(self.state_file.read_text())
        except FileNotFoundError:
            return {}

    def message(self):
        return next(iter(self.snapshot().get("messages") or ()), None)

    def next_action(self, role, body):
        self.steps[role] += 1
        step = self.steps[role]
        tool_names = [
            (tool.get("name", ""), candidate.get("name", ""))
            for tool in body.get("tools", ()) if tool.get("type") == "namespace"
            for candidate in tool.get("tools", ())
        ] + [
            ("", tool.get("name", ""))
            for tool in body.get("tools", ()) if tool.get("type") == "function"
        ]
        normalized = {name.lower().replace(".", "_").replace("-", "_"): (space, name) for space, name in tool_names}

        def tool(suffix):
            return next((actual for key, actual in normalized.items() if key.endswith(suffix)), None)

        if role == "sender" and step == 1:
            recipient = next(session["id"] for session in self.snapshot()["sessions"] if session["name"] == "recipient")
            return step, require(tool("chat_send"), tool_names), {
                "to": recipient, "kind": "request", "body": "pilot-message", "idempotency_key": "pilot-send",
            }
        if step == 2:
            output = next((item.get("output") for item in body["input"] if item.get("call_id") == f"call_{role}_1"
                           and item.get("type") == "function_call_output"), None)
            result = json.dumps(output)
            expected = "pilot-message" if role == "sender" else "poll-only"
            if expected not in result:
                raise RuntimeError(f"{role} tool output lacked {expected}: {result[:500]}")
        if role == "recipient":
            if step == 1:
                return step, require(tool("chat_self"), tool_names), {}
            if step == 2:
                if not self.unblock.wait(30):
                    raise RuntimeError("recipient model did not see the bridge reconnect")
                return step, require(tool("chat_self"), tool_names), {}
            if step == 3:
                prompt = json.dumps(body["input"])
                message = self.message()
                assert "Jackstay session chat delivery" in prompt and message["id"] in prompt, "active turn did not receive the steer"
                return step, require(tool("chat_ack"), tool_names), {"message_id": message["id"]}
        return step, None, None


def require(tool, names):
    if not tool:
        raise RuntimeError(f"Jackstay MCP tool missing; model saw {names}")
    return tool


def stop(process):
    if process.poll() is None:
        os.killpg(process.pid, signal.SIGTERM)
        try:
            process.wait(timeout=3)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=3)


def start_process(stack, command, cwd, env, log):
    with log.open("ab") as output:
        process = subprocess.Popen(command, cwd=cwd, env=env, stdin=subprocess.PIPE, stdout=output,
                                   stderr=subprocess.STDOUT, start_new_session=True)
    stack.callback(stop, process)
    return process


def run(binary, root, stack):
    checkout = root / "repo"
    checkout.mkdir()
    subprocess.run(["git", "init", "-q", str(checkout)], check=True)
    (checkout / "jackstay.json").write_text(json.dumps({
        "jackstay_schema": 2, "project": "chat-pilot",
        "chat": {"enabled": True, "codex_bridge": {"enabled": True, "poll_interval_ms": 100, "ack_timeout_seconds": 5}},
    }))
    jackstay_home = root / "jackstay"
    namespace = hashlib.sha256(str((checkout / ".git").resolve()).encode()).hexdigest()
    script = Script(jackstay_home / ".all" / "session-chat" / namespace / "state.json")
    model = stack.enter_context(http.server.ThreadingHTTPServer(("127.0.0.1", 0), PilotModel))
    model.script = script
    threading.Thread(target=model.serve_forever, daemon=True).start()
    stack.callback(model.shutdown)
    environment = {"PATH": os.environ["PATH"], "HOME": str(root), "JACKSTAY_HOME": str(jackstay_home),
                   "RUST_LOG": "error", "PWD": str(checkout)}
    owners, config = {}, {}
    for role in ("recipient", "sender"):
        codex_home = root / role
        codex_home.mkdir()
        socket_path = root / (role + ".sock")
        token = secrets.token_hex(24)
        config[role] = (socket_path, token)
        tools = ["chat.self", "chat.ack"] if role == "recipient" else ["chat.send"]
        (codex_home / "config.toml").write_text(
            f'model = "pilot"\nmodel_provider = "local_pilot"\ncheck_for_update_on_startup = false\n'
            f'[model_providers.local_pilot]\nname = "Pilot"\n'
            f'base_url = "http://127.0.0.1:{model.server_port}/{role}/v1"\nwire_api = "responses"\n'
            f'[mcp_servers.jackstay_chat]\ncommand = {json.dumps(str(binary))}\n'
            f'args = ["chat", "serve", "--name", "{role}"]\ncwd = {json.dumps(str(checkout))}\n'
            f'env_vars = ["JACKSTAY_HOME", "JACKSTAY_CHAT_SESSION_TOKEN"]\nrequired = true\n'
            f'enabled_tools = {json.dumps(tools)}\ndefault_tools_approval_mode = "approve"\n'
        )
        process = start_process(stack, ["codex", "app-server", "--listen", "unix://" + str(socket_path)], checkout,
                                dict(environment, CODEX_HOME=str(codex_home), JACKSTAY_CHAT_SESSION_TOKEN=token),
                                root / (role + ".log"))
        if not wait_for(role + " socket", lambda: socket_path.exists() or process.poll() is not None) or not socket_path.exists():
            raise RuntimeError(role + " Codex did not start")
        owners[role] = AppServer(socket_path, stack)

    thread, turn = owners["recipient"].start(checkout, "Wait for a Jackstay chat request and acknowledge it.")
    wait_for("recipient MCP tool call", lambda: script.steps["recipient"] >= 2 or script.error)
    owners["sender"].start(checkout, "Send the pilot request using Jackstay chat.")
    sent = wait_for("sender MCP message", script.message)
    assert sent["body"] == "pilot-message" and not sent.get("acknowledged")

    socket_path, token = config["recipient"]
    bridge_command = [str(binary), "chat", "codex-bridge", "--socket", str(socket_path), "--thread", thread, "--name", "recipient"]
    bridge_env = dict(environment, JACKSTAY_CHAT_SESSION_TOKEN=token)
    bridge_log = root / "bridge.log"
    bridge = start_process(stack, bridge_command, checkout, bridge_env, bridge_log)
    accepted = wait_for("active-turn acceptance", lambda: script.message().get("codex_accepted"))
    assert accepted["turn_id"] == turn and accepted["thread_id"] == thread
    wait_for("acceptance diagnostic", lambda: "Codex accepted 1 message(s)" in bridge_log.read_text())
    os.killpg(bridge.pid, signal.SIGKILL)
    bridge.wait(timeout=3)
    assert not script.message().get("acknowledged")
    start_process(stack, bridge_command, checkout, bridge_env, bridge_log)
    wait_for("restarted bridge reads the pending ACK",
             lambda: "still pending recipient acknowledgment" in bridge_log.read_text())
    script.unblock.set()
    wait_for("recipient MCP acknowledgment after reconnect", lambda: script.message().get("acknowledged"))
    assert bridge_log.read_text().count("Codex accepted 1 message(s)") == 1
    print("PASS: two Codex threads, MCP send, active-turn steer, bridge restart, recipient MCP ACK")


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="jcp-", dir="/tmp", ignore_cleanup_errors=True) as directory:
        location = Path(directory)
        try:
            with ExitStack() as lifecycle:
                run(Path(sys.argv[1]).resolve(strict=True), location, lifecycle)
        except Exception:
            for logfile in location.glob("*.log"):
                print(logfile.name, logfile.read_text()[-3000:], file=sys.stderr)
            raise
