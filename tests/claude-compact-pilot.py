"""Exercise the installed Claude Code compact event against a local model fixture.

Usage: python3 tests/claude-compact-pilot.py /absolute/path/to/jst
"""

from contextlib import contextmanager
import http.server
import json
import os
from pathlib import Path
import queue
import shlex
import shutil
import subprocess
import sys
import tempfile
import threading
import time

MARKER = "synthetic-compact-next-action-839"


class Gateway(http.server.BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"data":[],"has_more":false}')

    def do_POST(self):
        value = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if "messages" not in value:
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.end_headers()
            self.wfile.write(b'{"input_tokens":24}')
            return
        responses = self.server.requests_seen
        responses.append(value)
        text = "Synthetic answer to the test request."
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        events = [
            ("message_start", {"message": {"id":f"msg_fixture_{len(responses)}","type":"message","role":"assistant",
              "model":value["model"],"stop_reason":None,"stop_sequence":None,"content":[],
              "usage":{"input_tokens":24,"output_tokens":1}}}),
            ("content_block_start", {"index":0,"content_block":{"type":"text","text":""}}),
            ("content_block_delta", {"index":0,"delta":{"type":"text_delta","text":text}}),
            ("content_block_stop", {"index":0}),
            ("message_delta", {"delta":{"stop_reason":"end_turn","stop_sequence":None},"usage":{"output_tokens":8}}),
            ("message_stop", {}),
        ]
        for event, data in events:
            self.wfile.write((f"event: {event}\ndata: " + json.dumps({"type":event,**data}) + "\n\n").encode())


@contextmanager
def claude_process(command, checkout, environment):
    child = subprocess.Popen(
        command, cwd=checkout, env=environment, stdin=subprocess.PIPE,
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, text=True,
    )
    try:
        yield child
    finally:
        child.stdin.close()
        try:
            child.wait(timeout=5)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait(timeout=5)
        child.stdout.close()


def run_pilot(root, binary):
    checkout = root / "repo"
    subprocess.run(["git", "init", "-q", "-b", "main", checkout], check=True)
    (checkout/"jackstay.json").write_text('{"jackstay_schema":2,"project":"compact-fixture"}\n')
    subprocess.run(["git","add","jackstay.json"], cwd=checkout, check=True)
    subprocess.run(["git", "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c",
                    "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "Synthetic fixture"], cwd=checkout, check=True)
    environment = {"PATH":os.environ["PATH"],"HOME":str(root),"CLAUDE_CONFIG_DIR":str(root/"claude"),
                   "JACKSTAY_HOME":str(root/"jst-state"),"JACKSTAY_TELEMETRY":"off",
                   "DISABLE_NONESSENTIAL_TRAFFIC":"1","DISABLE_TELEMETRY":"1","DISABLE_AUTOUPDATER":"1",
                   "ANTHROPIC_AUTH_TOKEN":"local-synthetic-fixture"}
    subprocess.run([binary,"brief","save","--expect","0"], cwd=checkout, env=environment, text=True, check=True,
                   input=json.dumps({"task_id":"synthetic","task":"Check the compact hook",
                                     "next":[MARKER],"decisions":[]}))
    wrapper = root / "hook.py"
    evidence = root / "hook-witness.json"
    wrapper.write_text(
        "import json, subprocess, sys\nfrom pathlib import Path\n"
        "raw = sys.stdin.buffer.read()\n"
        "event = json.loads(raw)\n"
        f"out = subprocess.run([{str(binary)!r}, 'brief', 'claude-hook'], input=raw, capture_output=True)\n"
        f"Path({str(evidence)!r}).write_text(json.dumps(dict(event=event.get('hook_event_name'), source=event.get('source'),"
        " delivered=bool(out.stdout), exit=out.returncode)))\n"
        "sys.stdout.buffer.write(out.stdout)\nsys.exit(out.returncode)\n"
    )
    settings = root / "settings.json"
    settings.write_text(json.dumps({"hooks":{"SessionStart":[{"matcher":"compact","hooks":[
        {"type":"command","command":shlex.join([sys.executable,str(wrapper)]),"timeout":10}
    ]}]}}))
    buffer = queue.Queue()
    with http.server.ThreadingHTTPServer(("127.0.0.1",0), Gateway) as server:
        server.requests_seen = []
        threading.Thread(target=server.serve_forever,daemon=True).start()
        environment["ANTHROPIC_BASE_URL"] = f"http://127.0.0.1:{server.server_port}"
        claude = shutil.which("claude")
        if claude is None:
            raise RuntimeError("Claude Code is required on PATH")
        command = [
            claude, "-p", "--verbose", "--input-format", "stream-json",
            "--output-format", "stream-json", "--model", "sonnet",
            "--setting-sources", "", "--settings", str(settings), "--tools", "",
            "--strict-mcp-config", "--mcp-config", '{"mcpServers":{}}',
        ]
        with claude_process(command, checkout, environment) as child:
            def read():
                for line in child.stdout:
                    try:
                        buffer.put(json.loads(line))
                    except ValueError:
                        continue
                buffer.put(None)
            threading.Thread(target=read,daemon=True).start()

            def turn(prompt):
                child.stdin.write(json.dumps({"type":"user","message":{"role":"user","content":prompt}})+"\n")
                child.stdin.flush()
                rows = []
                deadline = time.monotonic() + 25
                while time.monotonic() < deadline:
                    row = buffer.get(timeout=max(0,deadline-time.monotonic()))
                    if row is None:
                        raise RuntimeError(f"CLI closed before completing the turn, exit {child.poll()}, events {rows}")
                    rows.append((row.get("type"),row.get("subtype"),row.get("is_error")))
                    if row.get("type") == "result":
                        return rows, row.get("result","")
                raise RuntimeError(f"no completed turn; events {rows}")
            first, first_text = turn("This is a synthetic test conversation. Respond with one short line.")
            assert first[-1][-1] is False, (first, first_text)
            assert not evidence.exists(), "the compact hook ran before compaction"
            assert all(MARKER not in json.dumps(request) for request in server.requests_seen)
            compact, compact_text = turn("/compact")
            print("Compaction events:",compact)
            assert evidence.exists(), (compact, compact_text, len(server.requests_seen))
            witness=json.loads(evidence.read_text())
            assert witness=={"event":"SessionStart","source":"compact","delivered":True,"exit":0},witness
            assert any(subtype=="compact_boundary" for _,subtype,_ in compact),compact
            before=len(server.requests_seen)
            after, after_text = turn("Continue the synthetic conversation.")
            assert after[-1][-1] is False, (after,after_text)
            assert any(MARKER in json.dumps(value) for value in server.requests_seen[before:]), "brief did not reach subsequent model context"
        server.shutdown()
    print("PASS: real Claude Code /compact, hook source=compact, saved brief in next model request")


with tempfile.TemporaryDirectory(prefix="jst-compact-",dir="/tmp") as location:
    run_pilot(Path(location), Path(sys.argv[1]).resolve(strict=True))
