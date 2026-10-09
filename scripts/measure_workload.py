#!/usr/bin/env python3
"""Run an isolated Linux PTY workload; write JSON/profiles only under --output.

Example: python3 scripts/measure_workload.py /tmp/wstat --mode idle --output /tmp/run
CPU is percent of one core, measured from /proc; startup and steady state differ.
This script never discovers or modifies host web/FPM services.
"""
import argparse
import fcntl
import json
import os
import pty
import select
import signal
import struct
import tempfile
import termios
import time
from pathlib import Path


def record(i, path=None):
    ip = f"192.0.{(i-54000)//256 % 256}.{i % 256}" if path else f"10.0.{i // 256 % 44}.{i % 256}"
    status = 200 if path else 200 + i % 4 * 100
    path = path or f"/path-{i % 20000}"
    return (f'{ip} - - [09/Oct/2026:19:25:52 +0200] '
            f'"GET {path} HTTP/1.1" {status} 100 "-" "Mozilla/5.0"\n')


def proc(pid):
    fields = Path(f"/proc/{pid}/stat").read_text().rsplit(") ", 1)[1].split()
    ticks = int(fields[11]) + int(fields[12])
    rss = int(fields[21]) * os.sysconf("SC_PAGE_SIZE")
    return ticks, rss


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary")
    parser.add_argument("--mode", choices=["idle", "live", "burst"], default="idle")
    parser.add_argument("--seconds", type=float, default=10)
    parser.add_argument("--warmup", type=float, default=3)
    parser.add_argument("--output", required=True)
    parser.add_argument("--profile", action="store_true", help="requires fixed CLI profile flags")
    args = parser.parse_args()
    if args.seconds <= 0 or args.warmup <= 0:
        parser.error("durations must be positive")
    binary = str(Path(args.binary).resolve())
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    hz = os.sysconf("SC_CLK_TCK")
    with tempfile.TemporaryDirectory(prefix="wstat-workload-") as folder:
        root = Path(folder)
        files = []
        for i in range(63):
            suffix = "-ssl" if i >= 46 else ""
            path = root / f"host-{i % 46:02d}{suffix}-access.log"
            files.append(path)
        handles = [p.open("w") for p in files]
        for i in range(54000):
            handles[i % 63].write(record(i))
        for handle in handles:
            handle.close()
        (root / "wstat.toml").write_text(
            "[source]\nseed_lines = 1000\n[detect]\ncache = false\n[fpm]\nenabled = false\n")
        env = os.environ.copy()
        env.update(TERM="xterm-256color", GOMAXPROCS="4", XDG_CONFIG_HOME=str(root / "config"),
                   XDG_DATA_HOME=str(root / "data"), WSTAT_FPM_POOL_GLOB=str(root / "no-pools-*.conf"))
        env.pop("GOMEMLIMIT", None)
        pid, fd = pty.fork()
        if pid == 0:
            os.chdir(root)
            argv = [binary, "-n", "1000"]
            if args.profile:
                argv += ["-profile-after", f"{args.warmup}s",
                         "-cpuprofile", str(output / "cpu.pprof"),
                         "-heapprofile", str(output / "heap.pprof")]
            argv += [str(root / "*access.log")]
            os.execve(binary, argv, env)
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 50, 160, 0, 0))
        terminal = bytearray()
        def drain(timeout=0):
            if select.select([fd], [], [], timeout)[0]:
                try:
                    data = os.read(fd, 1 << 20)
                except OSError:
                    return False
                terminal.extend(data)
                if b"\x1b[6n" in data:  # terminal cursor query
                    os.write(fd, b"\x1b[1;1R")
                if b"\x1b[c" in data or b"\x1b[>0c" in data:
                    os.write(fd, b"\x1b[?1;2c\x1b[>0;0;0c")
                return bool(data)
            return True
        reaped = False
        try:
            launched = time.monotonic()
            while time.monotonic() - launched < args.warmup:
                drain(.05)
            ticks0, rss0 = proc(pid)
            started = time.monotonic()
            rate = {"idle": 0, "live": 100, "burst": 1000}[args.mode]
            emitted = 0
            peak = rss0
            with files[0].open("a", buffering=1) as writer:
                while time.monotonic() - started < args.seconds:
                    elapsed = time.monotonic() - started
                    wanted = int(elapsed * rate)
                    while emitted < wanted:
                        writer.write(record(54000 + emitted, f"/live-{emitted}"))
                        emitted += 1
                    drain(.01)
                    _, rss = proc(pid)
                    peak = max(peak, rss)
                ended = time.monotonic()
                ticks1, rss1 = proc(pid)
                sentinel = f"/verified-{emitted}"
                lag_start = time.monotonic()
                writer.write(record(1000000, sentinel))
                os.write(fd, b"4G\r")  # verify in the stream after the timed workload
            while sentinel.encode() not in terminal and time.monotonic() - lag_start < 3:
                drain(.05)
            seen = sentinel.encode() in terminal
            lag = time.monotonic() - lag_start
            os.write(fd, b"q")
            deadline = time.monotonic() + 5
            status = None
            while time.monotonic() < deadline:
                drain(.05)
                done, code = os.waitpid(pid, os.WNOHANG)
                if done:
                    reaped = True
                    status = os.waitstatus_to_exitcode(code)
                    break
            if status is None:
                raise RuntimeError("dashboard did not exit after q")
            result = dict(binary=binary, mode=args.mode, seed_requests=54000, sources=63,
                          hosts=46, terminal="160x50", refresh_ms=500, gomaxprocs=4,
                          warmup_seconds=started-launched, steady_seconds=ended-started,
                          startup_cpu_percent=ticks0/hz/(started-launched)*100,
                          steady_cpu_percent=(ticks1-ticks0)/hz/(ended-started)*100,
                          rss_start=rss0, rss_end=rss1, peak_rss=peak, live_emitted=emitted,
                          sentinel_visible=seen, sentinel_lag_seconds=lag, exit_code=status)
            (output / "result.json").write_text(json.dumps(result, indent=2) + "\n")
            (output / "terminal.txt").write_bytes(terminal)
            print(json.dumps(result))
            if status != 0 or not seen:
                raise RuntimeError("workload or final ingestion check failed")
        finally:
            try:
                if not reaped:
                    os.kill(pid, signal.SIGTERM)
                    until = time.monotonic() + 3
                    while time.monotonic() < until:
                        done, _ = os.waitpid(pid, os.WNOHANG)
                        if done:
                            reaped = True
                            break
                        time.sleep(.05)
                    if not reaped:
                        os.kill(pid,signal.SIGKILL)
                        os.waitpid(pid,0)
            except (ProcessLookupError, ChildProcessError):
                pass
            os.close(fd)


if __name__ == "__main__":
    main()
