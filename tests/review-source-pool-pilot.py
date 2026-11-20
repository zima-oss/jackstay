"""Exercise pinned, read-only source access through both installed review CLIs.

Usage: python3 tests/review-source-pool-pilot.py /absolute/path/to/jst
All model traffic goes to loopback fixtures; no account credentials are used.
"""

import http.server
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import threading


ANSWER = {"verdict": "approve", "summary": "Pinned source inspected", "findings": [], "prior": []}
START = "PINNED_HEAD_START_7c80fce9"
END = "PINNED_HEAD_END_918aff23"
REMOVED = "PINNED_BASE_REMOVED_d3724d29"
RENAMED = "PINNED_RENAMED_5327c5bc"
DIRTY = "UNCOMMITTED_CHECKOUT_ONLY_a53ca8e5"


def run(argv, cwd, env, timeout=120):
    result = subprocess.run(argv, cwd=cwd, env=env, text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"{argv[0]} exited {result.returncode}: {result.stdout[-1500:]} {result.stderr[-1500:]}")
    return result.stdout.strip()


def fixture(root, env):
    repo = root / "repo"
    repo.mkdir()

    def git(*args):
        return run(["git", *args], repo, env)

    git("init", "-q", "-b", "main")
    config = {"jackstay_schema": 3, "project": "source-pool-pilot", "review": {
        "reviewers": {
            "codex": {"command": shutil.which("codex"), "isolation": [
                "--ephemeral", "--ignore-rules", "--skip-git-repo-check",
                "-c", "mcp_servers={}", "-c", "features.apps=false",
                "-c", "shell_environment_policy.inherit=none",
            ]},
            "claude-code": {"command": str(root / "claude-reviewer")},
        },
    }}
    (repo / "jackstay.json").write_text(json.dumps(config))
    (repo / "old-name.txt").write_text("before rename\n")
    (repo / "removed.txt").write_text(REMOVED + "\n")
    (repo / "large.txt").write_text("base\n")
    git("add", ".")
    commit = ["-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
              "-c", "commit.gpgsign=false", "commit", "-q"]
    git(*commit, "-m", "Fixture base")
    base = git("rev-parse", "HEAD")
    git("mv", "old-name.txt", "new-name.txt")
    (repo / "new-name.txt").write_text("before rename\n" + RENAMED + "\n")
    (repo / "removed.txt").unlink()
    (repo / "large.txt").write_text(START + "\n" + ("tracked line for large review\n" * 7000) + END + "\n")
    (repo / "outside-link").symlink_to(root / "outside.txt")
    (root / "outside.txt").write_text("OUTSIDE_SECRET_SENTINEL_ee7f118a\n")
    git("add", "-A")
    git(*commit, "-m", "Fixture head")
    head = git("rev-parse", "HEAD")
    diff_bytes = len(subprocess.check_output(["git", "diff", base, head, "--", "large.txt"],
                                             cwd=repo, env=env, timeout=10))
    assert diff_bytes > 128 << 10
    (repo / "checkout-only.txt").write_text(DIRTY + "\n")
    return repo, base, head, diff_bytes


def sse_events(items):
    return b"".join(("data: " + json.dumps(item) + "\n\n").encode() for item in items)


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
        pilot = self.server.pilot
        try:
            if self.path.startswith("/codex/"):
                payload = pilot.codex_response(body)
            else:
                payload = pilot.claude_response(body)
        except Exception as error:
            pilot.errors.append(repr(error))
            self.send_error(400, "pilot assertion failed")
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        self.wfile.write(payload)
        self.wfile.flush()


class Pilot:
    def __init__(self, root):
        self.root = root
        self.errors = []
        self.requests = {"codex": [], "claude-code": []}
        self.observed = {"codex": set(), "claude-code": set()}
        self.cwd = {}

    def initial_request(self, reviewer, body):
        self.requests[reviewer].append(body)
        content = json.dumps(body.get("input", body.get("messages", [])))
        if len(self.requests[reviewer]) == 1:
            for marker in (START, END, REMOVED, RENAMED, DIRTY, "diff --git", "@@ -"):
                assert marker not in content, f"{reviewer} received source or patch in initial prompt: {marker}"
            assert len(content) < 128 << 10, f"{reviewer} initial prompt unexpectedly large"

    def check_output(self, reviewer, body):
        content = json.dumps(body.get("input", body.get("messages", [])))
        for marker in (START, END, REMOVED, RENAMED):
            if marker in content:
                self.observed[reviewer].add(marker)
        if "OUTSIDE_SECRET_SENTINEL" in content or DIRTY in content:
            raise AssertionError(f"{reviewer} read outside pinned source: {content[-1200:]}")

    def codex_response(self, body):
        reviewer = "codex"
        self.initial_request(reviewer, body)
        self.check_output(reviewer, body)
        step = len(self.requests[reviewer])
        feedback = json.dumps(body.get("input", []))
        if step == 2:
            assert all(marker in feedback for marker in ("PINNED_TREE_PRESENT", "LINK_INERT", "NO_GIT",
                                                          "DIRTY_EXCLUDED")), feedback[-2000:]
        if step in (3, 4, 5):
            assert "Operation not permitted" in feedback or "Permission denied" in feedback, feedback[-2000:]
        command = {
            1: "pwd; test ! -d .git && echo NO_GIT; test ! -e head/checkout-only.txt && echo DIRTY_EXCLUDED; "
               "test -f head/large.txt && echo PINNED_TREE_PRESENT; "
               "test ! -L head/outside-link && echo LINK_INERT; "
               "grep -n -E 'PINNED_HEAD_START|PINNED_HEAD_END' head/large.txt | cut -c1-140; "
               "cat base/removed.txt; cat head/new-name.txt; cat changes/base.paths; "
               "tail -n 3 changes/base.patch",
            2: "touch head/should-not-write.txt",
            3: "cat " + str(self.root / "outside.txt"),
            4: "cat " + str(self.root / "repo" / "checkout-only.txt"),
        }.get(step)
        if command:
            item = {"id": f"item_{step}", "type": "function_call", "call_id": f"call_{step}",
                    "name": "exec_command", "arguments": json.dumps({"cmd": command, "max_output_tokens": 2500}),
                    "status": "completed"}
        else:
            assert self.observed[reviewer] == {START, END, REMOVED, RENAMED}, self.observed[reviewer]
            item = {"id": f"item_{step}", "type": "message", "role": "assistant", "status": "completed",
                    "content": [{"type": "output_text", "text": json.dumps(ANSWER), "annotations": []}]}
        response = {"id": f"resp_{step}", "object": "response", "status": "completed", "output": [item],
                    "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2,
                              "input_tokens_details": {"cached_tokens": 0},
                              "output_tokens_details": {"reasoning_tokens": 0}}}
        return sse_events([{"type": "response.output_item.done", "item": item, "output_index": 0},
                           {"type": "response.completed", "response": response}])

    def claude_response(self, body):
        reviewer = "claude-code"
        self.initial_request(reviewer, body)
        self.check_output(reviewer, body)
        step = len(self.requests[reviewer])
        if step == 7:
            latest = json.dumps(body["messages"][-1])
            assert END in latest, f"Claude did not read the end of the pinned patch: {latest[-900:]}"
        cwd_file = self.root / "claude-cwd"
        assert cwd_file.exists(), "Claude wrapper did not record its cwd"
        source = Path(cwd_file.read_text().strip())
        tools = {tool["name"] for tool in body.get("tools", [])}
        assert "Bash" not in tools and "Write" not in tools and "Edit" not in tools, tools
        paths = {
            1: ("Read", {"file_path": str(source / "head" / "large.txt"), "offset": 1, "limit": 2}),
            2: ("Read", {"file_path": str(source / "head" / "large.txt"), "offset": 7001, "limit": 3}),
            3: ("Read", {"file_path": str(source / "base" / "removed.txt")}),
            4: ("Read", {"file_path": str(source / "head" / "new-name.txt")}),
            5: ("Read", {"file_path": str(source / "changes" / "base.paths")}),
            6: ("Read", {"file_path": str(source / "changes" / "base.patch"), "offset": 7003, "limit": 8}),
            7: ("Read", {"file_path": str(self.root / "repo" / "checkout-only.txt")}),
            8: ("Read", {"file_path": str(self.root / "outside.txt")}),
        }
        name, arguments = paths.get(step, (None, None))
        if name:
            assert name in tools, f"Claude does not expose {name}: {tools}"
            content = {"type": "tool_use", "id": f"toolu_fixture_{step}", "name": name, "input": {}}
            delta = {"type": "input_json_delta", "partial_json": json.dumps(arguments)}
        else:
            assert self.observed[reviewer] == {START, END, REMOVED, RENAMED}, self.observed[reviewer]
            assert "StructuredOutput" in tools, tools
            name = "StructuredOutput"
            content = {"type": "tool_use", "id": f"toolu_fixture_{step}", "name": name, "input": {}}
            delta = {"type": "input_json_delta", "partial_json": json.dumps(ANSWER)}
        events = [
            ("message_start", {"message": {"id": f"msg_fixture_{step}", "type": "message", "role": "assistant",
                                      "model": body["model"], "stop_reason": None, "stop_sequence": None,
                                      "usage": {"input_tokens": 1, "output_tokens": 1}, "content": []}}),
            ("content_block_start", {"index": 0, "content_block": content}),
            ("content_block_delta", {"index": 0, "delta": delta}),
            ("content_block_stop", {"index": 0}),
            ("message_delta", {"delta": {"stop_reason": "tool_use" if name != "StructuredOutput" else "end_turn",
                                          "stop_sequence": None}, "usage": {"output_tokens": 1}}),
            ("message_stop", {}),
        ]
        return b"".join((f"event: {event}\ndata: " + json.dumps({"type": event, **data}) + "\n\n").encode()
                        for event, data in events)


def prepare_wrappers(root, port):
    claude = shutil.which("claude")
    if not shutil.which("codex") or not claude:
        raise RuntimeError("installed codex and claude CLIs are required")
    codex_home = root / "codex-home"
    codex_home.mkdir()
    (codex_home / "auth.json").write_text('{"OPENAI_API_KEY":"synthetic-local-fixture"}')
    (codex_home / "config.toml").write_text(
        'model = "pilot"\nmodel_provider = "local_pilot"\ncheck_for_update_on_startup = false\n'
        '[model_providers.local_pilot]\nname = "Fixture"\n'
        f'base_url = "http://127.0.0.1:{port}/codex/v1"\nwire_api = "responses"\n')
    claude_wrapper = root / "claude-reviewer"
    claude_wrapper.write_text("#!/bin/sh\nset -eu\n"
                              f"pwd > {json.dumps(str(root / 'claude-cwd'))}\n"
                              f"export ANTHROPIC_BASE_URL=http://127.0.0.1:{port}\n"
                              "export ANTHROPIC_AUTH_TOKEN=synthetic-local-fixture\n"
                              f"{json.dumps(claude)} \"$@\" > {json.dumps(str(root / 'claude-raw'))}\n"
                              f"cat {json.dumps(str(root / 'claude-raw'))}\n")
    claude_wrapper.chmod(0o700)
    return codex_home


def main(binary):
    with tempfile.TemporaryDirectory(prefix="jst-source-pilot-", dir="/tmp") as location:
        root = Path(location)
        pilot = Pilot(root)
        with http.server.ThreadingHTTPServer(("127.0.0.1", 0), Gateway) as server:
            server.pilot = pilot
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                codex_home = prepare_wrappers(root, server.server_port)
                env = {"PATH": os.environ["PATH"], "HOME": str(root), "CODEX_HOME": str(codex_home),
                       "CLAUDE_CONFIG_DIR": str(root / "claude-home"), "JACKSTAY_HOME": str(root / "jst"),
                       "DISABLE_TELEMETRY": "1", "DISABLE_NONESSENTIAL_TRAFFIC": "1",
                       "DISABLE_AUTOUPDATER": "1", "ENABLE_TOOL_SEARCH": "false"}
                repo, base, head, diff_bytes = fixture(root, env)
                run([str(binary), "review", "pool", "add", "--repo", str(repo), "--base", base,
                     "--head", head, "--task", "source-pilot", "--reviewer", "codex,claude-code"], repo, env)
                outcome = run([str(binary), "review", "pool", "next"], repo, env, timeout=180)
                jobs = json.loads(run([str(binary), "review", "pool", "list", "--json"], repo, env))
                assert len(jobs) == 1 and jobs[0]["status"] == "complete" and jobs[0]["exit"] == 0, (jobs, outcome)
                assert not pilot.errors, pilot.errors
                expected = {START, END, REMOVED, RENAMED}
                assert all(markers == expected for markers in pilot.observed.values()), pilot.observed
                claude_result = json.loads((root / "claude-raw").read_text())
                denied = {entry.get("tool_use_id") for entry in claude_result.get("permission_denials", [])}
                assert {"toolu_fixture_7", "toolu_fixture_8"} <= denied, claude_result
                assert run(["git", "status", "--porcelain"], repo, env) == "?? checkout-only.txt"
                print(f"PASS: installed Codex and Claude Code reviewed {diff_bytes} diff bytes through pinned pool")
            except Exception:
                print(f"pilot model errors: {pilot.errors}", file=sys.stderr)
                print(f"model request counts: {[len(pilot.requests[key]) for key in pilot.requests]}", file=sys.stderr)
                for artifact in (root / "jst").rglob("run.json"):
                    print(f"{artifact}: {artifact.read_text()[-1800:]}", file=sys.stderr)
                for artifact in (root / "jst").rglob("stderr.txt"):
                    print(f"{artifact}: {artifact.read_text()[-1800:]}", file=sys.stderr)
                raw = root / "claude-raw"
                if raw.exists():
                    print(f"Claude raw: {raw.read_text()[-1800:]}", file=sys.stderr)
                raise
            finally:
                server.shutdown()


if __name__ == "__main__":
    main(Path(sys.argv[1]).resolve(strict=True))
