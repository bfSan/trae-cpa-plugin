#!/usr/bin/env python3
"""Build a CPA store archive with one Trae library, no sibling plugins."""
from pathlib import Path
import hashlib
import re
import sys
import zipfile

version, goos, goarch = sys.argv[1:]
assert re.fullmatch(r"\d+\.\d+\.\d+", version)
assert goos in ("linux", "darwin", "windows") and goarch in ("amd64", "arm64")
root = Path(__file__).resolve().parents[1]
ext = {"linux": "so", "darwin": "dylib", "windows": "dll"}[goos]
lib = root / "dist" / f"{goos}-{goarch}" / f"trae.{ext}"
assert lib.is_file()
archive = root / "dist" / f"trae_{version}_{goos}_{goarch}.zip"
with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as z:
    z.write(lib, lib.name)
checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
archive.with_suffix(".sha256").write_text(f"{checksum}  {archive.name}\n")
print(archive)
