#!/usr/bin/env python3
"""Exercise a fixture revision against an existing test in a second Git worktree."""
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
from urllib.request import urlopen

ROOT = Path(__file__).resolve().parents[1]
FIXTURE = Path("internal/webhook/testdata/pull_request_opened.json")


def run(cwd, *args, env=None):
    return subprocess.check_output(args, cwd=cwd, env=env, text=True).strip()


def trial(tmp, env):
    producer, consumer = tmp / "producer", tmp / "consumer"
    run(tmp, "git", "clone", "--quiet", "--no-hardlinks", str(ROOT), str(producer))
    run(producer, "git", "worktree", "add", "--quiet", "--detach", str(consumer), "HEAD")

    def artifact(cwd, action, *args):
        return json.loads(run(cwd, ROOT / "bin/taaro", "artifact", action,
                              "--repo", "pilot/exchange", "--key", "webhook/opened",
                              "--json", *args, env=env))

    # The compiled test opens testdata relative to its working directory.
    # Give it the delivered file without overwriting any tracked consumer files.
    test_dir = tmp / "compatibility"
    (test_dir / "testdata").mkdir(parents=True)
    run(consumer, "go", "test", "-c", "-o", str(test_dir / "webhook.test"),
        "./internal/webhook", env=env)
    env["PILOT_TEST_DIR"] = str(test_dir)
    command = ('cd "$PILOT_TEST_DIR" && cp "$TAARO_FIXTURE_PATH" testdata/pull_request_opened.json '
               '&& ./webhook.test -test.run "^TestWebhookPullRequestHandoffAndDeduplication$" -test.v')
    for version in (1, 2):
        if version == 2:
            path = producer / FIXTURE
            payload = json.loads(path.read_text())
            payload["pull_request"]["title"] += " (revised)"
            path.write_text(json.dumps(payload, indent=2) + "\n")
            run(producer, "git", "-c", "user.name=Taaro pilot", "-c",
                "user.email=pilot@example.invalid", "commit", "-qm", "Revise pilot fixture", str(FIXTURE))
        published = artifact(producer, "publish", "--file", str(FIXTURE))
        if version == 2:
            assert any(s["worker_id"] == "consumer" for s in published["stale_consumers"])
        result = artifact(consumer, "rerun", "--version", str(version), "--command",
                          command, "--record-check")
        check = result["recorded_check"]
        assert check["conclusion"] == "success" and check["artifact_version"] == version
        assert check["producer_rev"] == run(producer, "git", "rev-parse", "HEAD")
        assert check["consumer_rev"] == run(consumer, "git", "rev-parse", "HEAD")
        assert "PASS: TestWebhookPullRequestHandoffAndDeduplication" in result["output"]
        subscription = artifact(consumer, "subscribe", "--worker", "consumer",
                                "--consumed-version", str(version))
        assert subscription["consumed_version"] == subscription["latest_version"] == version
        print(f'Fixture v{version} passed  producer={check["producer_rev"]} consumer={check["consumer_rev"]}')


def main():
    with tempfile.TemporaryDirectory(prefix="taaro-exchange-") as directory:
        tmp = Path(directory)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            addr = f"127.0.0.1:{listener.getsockname()[1]}"
        base = "http://" + addr
        env = {k: v for k, v in os.environ.items() if not k.startswith("TAARO_")}
        env.update(TAARO_LISTEN_ADDR=addr, TAARO_PUBLIC_URL=base, TAARO_SERVER_URL=base,
                   TAARO_DB_PATH=str(tmp / "pilot.db"), TAARO_API_TOKEN=secrets.token_hex(),
                   TAARO_WEBHOOK_SECRET=secrets.token_hex())
        server = subprocess.Popen([ROOT / "bin/taaro-server", "-env", ""], cwd=tmp,
                                  env=env, stdout=subprocess.DEVNULL)
        try:
            for _ in range(100):
                if server.poll() is not None:
                    raise RuntimeError("Pilot server failed to start")
                try:
                    with urlopen(base + "/healthz", timeout=1):
                        break
                except OSError:
                    time.sleep(0.05)
            else:
                raise RuntimeError("Pilot server failed its health check")
            trial(tmp, env)
        finally:
            server.terminate()
            server.wait(timeout=15)


if __name__ == "__main__":
    main()
