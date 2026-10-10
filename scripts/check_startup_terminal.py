#!/usr/bin/env python3
"""Check loading/quit in a Linux PTY using only temporary logs and config.

python3 scripts/check_startup_terminal.py /tmp/wstat --output /tmp/startup-check
A FIFO delays config reading to observe animation before the UI starts.
"""
import argparse
import errno
import fcntl
import json
import os
import pty
import re
import select
import signal
import struct
import tempfile
import termios
import time
from pathlib import Path

ANSI = re.compile(rb"\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07]*(?:\x07|\x1b\\)|[>=])")


def check(binary, output, invalid=False):
    with tempfile.TemporaryDirectory(prefix="wstat-startup-") as folder:
        root = Path(folder)
        log = root / "otter-access.log"
        log.write_text('192.0.2.1 - - [10/Oct/2026:12:00:00 +0200] "GET /animals/otter HTTP/1.1" 200 100 "-" "Mozilla/5.0"\n')
        config = root / "config.toml"
        os.mkfifo(config, 0o600)
        env = os.environ.copy()
        env.update(TERM="xterm-256color", XDG_CONFIG_HOME=str(root / "config"), XDG_DATA_HOME=str(root / "data"))
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(root)
            os.execve("/bin/sh", ["sh", "-c", '"$1" -config "$2"; result=$?; printf "NEXT_PROMPT> "; exit "$result"',
                                  "wstat-check", binary, str(config)], env)
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 160, 0, 0))
        data = bytearray()
        reaped = False

        def drain(seconds):
            deadline = time.monotonic() + seconds
            while time.monotonic() < deadline:
                if select.select([fd], [], [], 0.02)[0]:
                    try:
                        chunk = os.read(fd, 1 << 20)
                    except OSError:
                        break
                    if not chunk:
                        break
                    data.extend(chunk)
                    if b"\x1b[6n" in chunk:
                        os.write(fd, b"\x1b[1;1R")
                    if b"\x1b[c" in chunk or b"\x1b[>0c" in chunk:
                        os.write(fd, b"\x1b[?1;2c\x1b[>0;0;0c")

        try:
            # Nonblocking writer opens only once the CLI is reading the FIFO.
            deadline = time.monotonic() + 5
            writer = None
            while writer is None:
                try:
                    writer = os.open(config, os.O_WRONLY | os.O_NONBLOCK)
                except OSError as err:
                    if err.errno != errno.ENXIO or time.monotonic() > deadline:
                        raise
                    drain(0.02)
            drain(0.55)
            frames = re.findall(rb"wstat: loading config / detection ([|/\\-])", data)
            assert len(set(frames)) >= 3, "Loading line did not animate during config reading"
            assert b"HOSTS" not in data, "Dashboard started before delayed config read"
            config_text = "invalid = [\n" if invalid else ('[source]\npaths = [' + json.dumps(str(log)) + ']\nseed_lines = 1\n'
                                                         '[detect]\ncache = false\n[fpm]\nenabled = false\n')
            os.write(writer, config_text.encode())
            os.close(writer)
            deadline = time.monotonic() + 5
            while (b"wstat config:" if invalid else b"HOSTS") not in data:
                assert time.monotonic() < deadline, "CLI did not finish startup"
                drain(0.05)
            if invalid:
                assert b"\r\x1b[2Kwstat config:" in data, "Loader did not clear before the error"
            else:
                assert b"\r\x1b[2K\x1b" in data, "Loader did not clear before terminal handoff"
                os.write(fd, b"q")
            deadline = time.monotonic() + 5
            while b"NEXT_PROMPT> " not in data:
                assert time.monotonic() < deadline, "CLI did not return to the shell"
                drain(0.05)
            _, status = os.waitpid(pid, 0)
            reaped = True
            assert os.waitstatus_to_exitcode(status) == (1 if invalid else 0)
            assert ANSI.sub(b"", bytes(data)).endswith(b"\r\nNEXT_PROMPT> "), "Shell prompt lacks a fresh line"
            if invalid:
                assert b"loading" not in bytes(data).split(b"wstat config:", 1)[1], "Loader kept writing after error"
            if not invalid:
                tail = bytes(data).rsplit(b"\x1b[?1049l", 1)
                assert len(tail) == 2, "Dashboard did not leave the alternate screen"
                assert ANSI.sub(b"", tail[1]).endswith(b"\r\nNEXT_PROMPT> "), "Shell prompt lacks a fresh line after q"
            (output / ("error.terminal" if invalid else "quit.terminal")).write_bytes(data)
            return {"case": "config_error" if invalid else "quit", "animated_frames": len(frames), "cleared": True,
                    "exit_code": os.waitstatus_to_exitcode(status), "shell_prompt_on_new_line": True}
        finally:
            if not reaped:
                try:
                    os.killpg(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                os.waitpid(pid, 0)
            os.close(fd)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    results = [check(str(Path(args.binary).resolve()), args.output, invalid) for invalid in (False, True)]
    (args.output / "result.json").write_text(json.dumps(results, indent=2) + "\n")
    print(json.dumps(results))


if __name__ == "__main__":
    main()
