#!/usr/bin/env python3
"""Verify downloaded Linux release archives locally, without extracting paths.

Usage: python3 scripts/verify_release.py ASSET_DIR VERSION FULL_COMMIT_SHA
Requires Go for build metadata; runs --version only on the native architecture.
Writes verification.json and extracted binaries/build metadata in ASSET_DIR.
"""
import argparse
import hashlib
import json
import platform
import struct
import subprocess
import tarfile
from pathlib import Path


def verify(root, version, revision):
    expected = {}
    for line in (root / "checksums.txt").read_text().splitlines():
        digest, name = line.split(maxsplit=1)
        expected[name.lstrip("*")] = digest
    archives = {
        f"wstat_{version}_linux_aarch64.tar.gz": ("arm64", 183),
        f"wstat_{version}_linux_x86_64.tar.gz": ("amd64", 62),
    }
    if set(expected) != set(archives):
        raise ValueError(f"Unexpected checksum entries: {sorted(expected)}")
    native = {"aarch64": "arm64", "arm64": "arm64", "x86_64": "amd64", "AMD64": "amd64"}.get(platform.machine())
    results = []
    for name, (arch, machine) in archives.items():
        digest = hashlib.sha256((root / name).read_bytes()).hexdigest()
        if digest != expected[name]:
            raise ValueError(f"Checksum mismatch: {name}")
        with tarfile.open(root / name, "r:gz") as archive:
            members = [m for m in archive.getmembers() if Path(m.name).name == "wstat" and m.isfile()]
            if len(members) != 1:
                raise ValueError(f"Expected one wstat binary: {name}")
            data = archive.extractfile(members[0]).read()
        if data[:6] != b"\x7fELF\x02\x01" or struct.unpack_from("<H", data, 18)[0] != machine:
            raise ValueError(f"Incorrect ELF architecture: {name}")
        offset = struct.unpack_from("<Q", data, 32)[0]
        size, count = struct.unpack_from("<HH", data, 54)
        if any(struct.unpack_from("<I", data, offset + i * size)[0] == 3 for i in range(count)):
            raise ValueError(f"Binary requires a dynamic interpreter: {name}")
        folder = root / arch
        folder.mkdir(exist_ok=True)
        binary = folder / "wstat"
        binary.write_bytes(data)
        binary.chmod(0o755)
        info = subprocess.check_output(["go", "version", "-m", str(binary)], text=True, timeout=30)
        (folder / "build-info.txt").write_text(info)
        required = ["CGO_ENABLED=0", "GOOS=linux", f"GOARCH={arch}", f"vcs.revision={revision}", "vcs.modified=false"]
        if any(item not in info for item in required):
            raise ValueError(f"Incorrect build metadata: {name}\n{info}")
        result = {"archive": name, "sha256": digest, "architecture": arch, "static": True, "revision": revision}
        if arch == native and platform.system() == "Linux":
            output = subprocess.check_output([str(binary), "--version"], text=True, timeout=10).strip()
            if output != f"wstat {version}":
                raise ValueError(f"Incorrect version: {output}")
            result["version_output"] = output
        results.append(result)
    (root / "verification.json").write_text(json.dumps(results, indent=2) + "\n")
    return results


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("asset_dir", type=Path)
    parser.add_argument("version")
    parser.add_argument("revision", help="Full Git commit SHA")
    args = parser.parse_args()
    print(json.dumps(verify(args.asset_dir.resolve(), args.version, args.revision)))
