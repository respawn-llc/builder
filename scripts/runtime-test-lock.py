#!/usr/bin/env python3

"""Run one runtime-package test command at a time on the local host."""

import os
import stat
import subprocess
import sys
import time


def repository_lock_path() -> str:
    result = subprocess.run(
        ["git", "rev-parse", "--git-common-dir"],
        capture_output=True,
        check=False,
        text=True,
    )
    if result.returncode != 0:
        raise RuntimeError(
            "resolve shared runtime test lock directory: "
            + result.stderr.strip()
        )
    common_dir = result.stdout.strip()
    if not common_dir:
        raise RuntimeError("resolve shared runtime test lock directory: empty git common directory")
    if not os.path.isabs(common_dir):
        common_dir = os.path.abspath(common_dir)
    if not os.path.isdir(common_dir):
        raise RuntimeError(
            f"resolve shared runtime test lock directory: not a directory: {common_dir}"
        )
    return os.path.join(common_dir, "kent-runtime-test.lock")


def open_shared_lock():
    lock_path = repository_lock_path()
    flags = os.O_RDWR | getattr(os, "O_CLOEXEC", 0) | getattr(os, "O_NOFOLLOW", 0)
    try:
        descriptor = os.open(lock_path, flags | os.O_CREAT | os.O_EXCL, 0o600)
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
