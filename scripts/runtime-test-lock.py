#!/usr/bin/env python3

"""Run one runtime-package test command at a time on the local host."""

import os
import subprocess
import sys
import tempfile
import time


def acquire_lock(lock_file) -> None:
    try:
        import fcntl
    except ImportError:
        import msvcrt

        lock_file.write(b"\0")
        lock_file.flush()
        while True:
            try:
                lock_file.seek(0)
                msvcrt.locking(lock_file.fileno(), msvcrt.LK_NBLCK, 1)
                return
            except OSError:
                print("waiting for shared runtime test admission...", file=sys.stderr, flush=True)
                time.sleep(0.1)
    else:
        try:
            fcntl.flock(lock_file.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            print("waiting for shared runtime test admission...", file=sys.stderr, flush=True)
            fcntl.flock(lock_file.fileno(), fcntl.LOCK_EX)


def main() -> int:
    command = sys.argv[1:]
    if not command:
        raise SystemExit("runtime test lock requires a command")

    lock_path = os.path.join(tempfile.gettempdir(), "kent-runtime-test.lock")
    with open(lock_path, "a+b", buffering=0) as lock_file:
        acquire_lock(lock_file)
        return subprocess.run(command).returncode


if __name__ == "__main__":
    raise SystemExit(main())
