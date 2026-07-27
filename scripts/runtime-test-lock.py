#!/usr/bin/env python3

"""Run one runtime-package test command at a time on the local host."""

import os
import stat
import subprocess
import sys
import tempfile
import time


def open_shared_lock():
    lock_path = "/tmp/kent-runtime-test.lock"
    if os.name == "nt":
        lock_path = os.path.join(tempfile.gettempdir(), "kent-runtime-test.lock")

    flags = os.O_RDWR | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        previous_umask = os.umask(0)
        try:
            descriptor = os.open(lock_path, flags | os.O_CREAT | os.O_EXCL, 0o666)
        finally:
            os.umask(previous_umask)
    except FileExistsError:
        descriptor = os.open(lock_path, flags)

    if not stat.S_ISREG(os.fstat(descriptor).st_mode):
        os.close(descriptor)
        raise RuntimeError(f"runtime test lock is not a regular file: {lock_path}")
    return os.fdopen(descriptor, "r+b", buffering=0)


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

    with open_shared_lock() as lock_file:
        acquire_lock(lock_file)
        return subprocess.run(command).returncode


if __name__ == "__main__":
    raise SystemExit(main())
