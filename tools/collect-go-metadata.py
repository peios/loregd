#!/usr/bin/env python3
"""Collect the exact Go source and complete vendored licence closure.

The script consumes ``go list -deps -json`` rather than copying the complete
vendor tree. This keeps the debugsource package tied to files the compiler
could actually mention. Licence checks cover every module in modules.txt,
including target-conditional modules not linked on the build architecture.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import sys


SOURCE_FIELDS = (
    "GoFiles",
    "CgoFiles",
    "CFiles",
    "CXXFiles",
    "MFiles",
    "HFiles",
    "FFiles",
    "SFiles",
    "SwigFiles",
    "SwigCXXFiles",
    "SysoFiles",
    "EmbedFiles",
)
LICENCE_NAMES = ("license", "licence", "copying", "notice", "patents")


def records(raw: str):
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(raw):
        while offset < len(raw) and raw[offset].isspace():
            offset += 1
        if offset == len(raw):
            return
        value, offset = decoder.raw_decode(raw, offset)
        yield value


def safe_relative(value: str) -> PurePosixPath:
    path = PurePosixPath(value)
    if path.is_absolute() or ".." in path.parts or not path.parts:
        raise ValueError(f"unsafe package path: {value!r}")
    return path


def copy_unique(source: Path, destination: Path) -> None:
    if destination.exists():
        if destination.read_bytes() != source.read_bytes():
            raise RuntimeError(f"source collision at {destination}")
        return
    destination.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, destination)


def module_root(module: dict, package_dir: Path, import_path: str) -> Path:
    replacement = module.get("Replace")
    if replacement and replacement.get("Dir"):
        return Path(replacement["Dir"])
    if module.get("Dir"):
        return Path(module["Dir"])

    # In vendor mode Go does not always expose Module.Dir. Derive the root
    # from the import path and package directory, then verify the relationship.
    module_path = module["Path"]
    if import_path == module_path:
        return package_dir
    prefix = module_path + "/"
    if not import_path.startswith(prefix):
        raise RuntimeError(
            f"package {import_path} is not below its module {module_path}"
        )
    suffix = PurePosixPath(import_path[len(prefix) :])
    root = package_dir
    for _ in suffix.parts:
        root = root.parent
    return root


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--debug-root", required=True, type=Path)
    parser.add_argument("--licence-root", required=True, type=Path)
    parser.add_argument("packages", nargs="+")
    args = parser.parse_args()

    proc = subprocess.run(
        ["go", "list", "-deps", "-json", *args.packages],
        check=True,
        stdout=subprocess.PIPE,
        text=True,
    )
    packages = list(records(proc.stdout))
    args.debug_root.mkdir(parents=True, exist_ok=True)
    args.licence_root.mkdir(parents=True, exist_ok=True)

    modules: dict[str, tuple[dict, Path, str]] = {}
    for package in packages:
        import_path = package.get("ImportPath")
        directory = package.get("Dir")
        if not import_path or not directory or import_path in ("C", "unsafe"):
            continue
        relative_import = safe_relative(import_path)
        source_dir = Path(directory)
        for field in SOURCE_FIELDS:
            for name in package.get(field, []):
                source = source_dir / name
                if not source.is_file():
                    raise RuntimeError(f"listed Go source is missing: {source}")
                copy_unique(source, args.debug_root / relative_import / name)

        module = package.get("Module")
        if module and not module.get("Main") and module.get("Path"):
            modules.setdefault(module["Path"], (module, source_dir, import_path))

    goroot = Path(subprocess.check_output(["go", "env", "GOROOT"], text=True).strip())
    go_licence = goroot / "LICENSE"
    if not go_licence.is_file():
        raise RuntimeError(f"Go toolchain licence is missing: {go_licence}")
    copy_unique(go_licence, args.licence_root / "go" / "LICENSE")
    go_patents = goroot / "PATENTS"
    if go_patents.is_file():
        copy_unique(go_patents, args.licence_root / "go" / "PATENTS")

    gomod = subprocess.check_output(["go", "env", "GOMOD"], text=True).strip()
    vendor = Path(gomod).parent / "vendor"
    modules_file = vendor / "modules.txt"
    if not modules_file.is_file():
        raise RuntimeError(f"vendor manifest is missing: {modules_file}")
    copy_unique(modules_file, args.licence_root / "modules.txt")

    roots: dict[str, Path] = {}
    for module_path, (module, package_dir, import_path) in modules.items():
        roots[module_path] = module_root(module, package_dir, import_path)
    for line in modules_file.read_text().splitlines():
        if not line.startswith("# ") or " => " in line:
            continue
        fields = line[2:].split()
        if len(fields) < 2:
            continue
        module_path = fields[0]
        roots.setdefault(module_path, vendor / safe_relative(module_path))

    missing = []
    for module_path, root in sorted(roots.items()):
        if not root.is_dir():
            missing.append(module_path)
            continue
        found = []
        for current, dirs, files in os.walk(root):
            dirs.sort()
            for name in sorted(files):
                if name.lower().startswith(LICENCE_NAMES):
                    found.append(Path(current) / name)
        if not found:
            missing.append(module_path)
            continue
        destination_root = args.licence_root / safe_relative(module_path)
        for source in found:
            copy_unique(source, destination_root / source.relative_to(root))

    if missing:
        print(
            "missing distributable licence text for Go module(s): "
            + ", ".join(missing),
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
