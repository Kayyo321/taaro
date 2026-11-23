"""Test controller ordering without making Codex model calls."""

import argparse
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


SCRIPT = Path(__file__).with_name("taaro-codex.py")
SPEC = importlib.util.spec_from_file_location("taaro_codex", SCRIPT)
controller = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(controller)

EVIDENCE = {
    "kind": "proposal", "key": "test", "version": "v2",
    "lifecycle_state": "accepted", "quoted_data": True,
}
PROTECTED = {"decision": "allow", "receipt_verified": True}
HELD = {"decision": "hold", "require_fresh_turn": True, "discard_pending_edit": True}


class Client:
    def __init__(self, reads, decisions):
        self.reads = iter(reads)
        self.decisions = iter(decisions)
        self.scopes = []
        self.receipts = []

    def post(self, endpoint, payload):
        if endpoint == "scopes":
            self.scopes.append(payload)
            return {}
        if endpoint == "awareness/receipt":
            self.receipts.append(payload)
            return {"id": len(self.receipts), "evidence_set_hash": "current"}
        if endpoint == "awareness/evaluate":
            if payload["operation"] == "read":
                evidence = next(self.reads)
                return {
                    "decision": "allow_with_context" if evidence else "allow",
                    "evidence": evidence, "evidence_set_hash": "current",
                }
            return next(self.decisions)
        raise AssertionError(endpoint)


class ControllerTest(unittest.TestCase):
    def test_post_stage_hold_discards_staged_edit(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / "target.txt"
            target.write_text("original\n")
            args = argparse.Namespace(repo="org/test", codex="codex", edit_mode="shell")
            server = Client([[], [EVIDENCE]], [PROTECTED, HELD, PROTECTED, PROTECTED])

            def propose(_codex, _task, _path, _source, evidence, _operation, _edit_mode):
                self.assertEqual(target.read_text(), "original\n")
                if evidence:
                    return "second-thread", "command from fresh turn"
                return "first-thread", "discarded command"

            with (
                patch.object(controller, "propose", side_effect=propose),
                patch("shell_stage.shell_replacement", side_effect=[b"stale\n", b"current\n"]),
            ):
                controller.mediate(args, server, target.name, target, "edit", "write")
            self.assertEqual(target.read_text(), "current\n")
            self.assertEqual(len(server.receipts), 1)
            self.assertEqual(server.receipts[0]["context_generation"], 2)

    def test_delegation_records_independent_receipts(self):
        with tempfile.TemporaryDirectory() as temporary:
            worktree = Path(temporary)
            target = worktree / "target.txt"
            target.write_text("original\n")
            args = argparse.Namespace(
                repo="org/test", codex="codex", edit_mode="text", delegate=True,
                worktree=worktree, path=target.name, task="edit", server="http://127.0.0.1",
            )
            server = Client([[EVIDENCE], [EVIDENCE]], [PROTECTED, PROTECTED])
            with (
                patch.object(controller, "Client", return_value=server),
                patch.object(controller, "propose", side_effect=[("planner", "child task"), ("editor", "edited\n")]),
            ):
                controller.run(args)
            self.assertEqual(target.read_text(), "edited\n")
            parent, child = server.scopes
            self.assertNotEqual(parent["worker_id"], child["worker_id"])
            self.assertEqual(child["parent_worker_id"], parent["worker_id"])
            self.assertEqual(
                [receipt["worker_id"] for receipt in server.receipts],
                [parent["worker_id"], child["worker_id"]],
            )


if __name__ == "__main__":
    unittest.main()
