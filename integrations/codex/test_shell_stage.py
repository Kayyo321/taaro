import os
from pathlib import Path
import shlex
import shutil
import tempfile
import unittest
from unittest.mock import patch

from shell_stage import shell_replacement


@unittest.skipUnless(shutil.which("bwrap"), "Linux shell staging requires bubblewrap")
class ShellStageTest(unittest.TestCase):
    def test_child_can_write_staged_file_only(self):
        with tempfile.TemporaryDirectory() as directory:
            host_file = Path(directory) / "original"
            host_file.write_text("original")
            command = (
                'test -z "${TAARO_API_TOKEN:-}" && '
                'test ! -e /proc && test ! -e /home && '
                f'test ! -e {shlex.quote(str(host_file))} && '
                """sh -c 'tr a-z A-Z < target > copy; cat copy > target'"""
            )
            with patch.dict(os.environ, TAARO_API_TOKEN="test-only"):
                self.assertEqual(shell_replacement(command, b"revision", 65536), b"REVISION")
            self.assertEqual(host_file.read_text(), "original")

    def test_failed_subprocess_discards_partial_edit(self):
        with self.assertRaisesRegex(ValueError, "exit 7"):
            shell_replacement("""sh -c 'echo partial > target; exit 7'""", b"old", 65536)

    def test_rejects_oversized_output(self):
        with self.assertRaisesRegex(ValueError, "invalid target"):
            shell_replacement("echo too-long >target", b"old", 3)
