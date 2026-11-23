"""Execute a proposed shell edit on a private copy of one file (Linux)."""

import os
from pathlib import Path
import resource
import shutil
import stat
import subprocess
import tempfile


def process_limit():
    # Linux charges RLIMIT_NPROC against all threads of the real user.
    threads = 0
    for status in Path("/proc").glob("[0-9]*/status"):
        try:
            fields = dict(line.split(":", 1) for line in status.read_text().splitlines() if ":" in line)
            if int(fields["Uid"].split()[0]) == os.getuid():
                threads += int(fields["Threads"])
        except (FileNotFoundError, ProcessLookupError, PermissionError):
            continue
    return threads + 32


def restrict_resources(processes):
    # The same hard limits apply to the shell and its descendants.
    for kind, limit in ((resource.RLIMIT_FSIZE, 65536), (resource.RLIMIT_CPU, 8),
                        (resource.RLIMIT_AS, 256 * 1024 * 1024),
                        (resource.RLIMIT_NOFILE, 64), (resource.RLIMIT_NPROC, processes), (resource.RLIMIT_CORE, 0)):
        inherited, _ = resource.getrlimit(kind)
        if inherited != resource.RLIM_INFINITY:
            limit = min(limit, inherited)
        resource.setrlimit(kind, (limit, limit))


def shell_replacement(command, original, max_size):
    bwrap = shutil.which("bwrap")
    if not bwrap:
        raise OSError("shell edits require bubblewrap on Linux")
    with tempfile.TemporaryDirectory(prefix="taaro-stage-") as tmp:
        stage = Path(tmp)
        staged = stage / "target"
        staged.write_bytes(original)
        argv = [bwrap, "--unshare-all", "--unshare-user", "--die-with-parent", "--new-session",
                "--cap-drop", "ALL", "--disable-userns", "--clearenv",
                "--setenv", "PATH", "/usr/bin", "--setenv", "LC_ALL", "C",
                "--ro-bind", "/usr/bin", "/usr/bin", "--ro-bind", "/usr/lib", "/usr/lib",
                "--symlink", "usr/bin", "/bin", "--symlink", "usr/lib", "/lib"]
        if Path("/usr/lib64").is_dir():
            argv += ["--ro-bind", "/usr/lib64", "/usr/lib64", "--symlink", "usr/lib64", "/lib64"]
        # Only the file has a writable host binding. Renames and auxiliary files
        # live in a private tmpfs. The controller reads back the original inode.
        argv += ["--size", "1048576", "--tmpfs", "/work", "--bind", str(staged),
                 "/work/target", "--dev", "/dev", "--chdir", "/work",
                 "--", "/bin/sh", "-eu", "-c", command]
        processes = process_limit()
        proc = subprocess.run(argv, env={}, cwd="/", stdin=subprocess.DEVNULL,
                              stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                              timeout=12, preexec_fn=lambda: restrict_resources(processes))
        if proc.returncode:
            raise ValueError(f"isolated shell edit failed with exit {proc.returncode}")
        with staged.open("rb") as result:
            info = os.fstat(result.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_size > max_size:
                raise ValueError("isolated shell produced an invalid target")
            content = result.read(max_size + 1)
            content.decode("utf-8")
            return content
