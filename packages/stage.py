#!/usr/bin/env python3
"""Stage and validate loregd without opening a hive or invoking Peios syscalls."""

import gzip
import hashlib
import json
import re
import shutil
import subprocess
import sys
from pathlib import Path


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def readelf(path: Path, option: str) -> str:
    return subprocess.check_output(["readelf", option, str(path)], text=True, errors="replace")


def identity(path: Path) -> str:
    matches = re.findall(r"Build ID: ([0-9a-f]+)", readelf(path, "-n"))
    require(len(matches) == 1, "missing or ambiguous GNU build identity")
    return matches[0]


def copy_file(source: Path, target: Path, executable: bool = False) -> None:
    target.parent.mkdir(parents=True, exist_ok=True)
    shutil.copyfile(source, target)
    target.chmod(0o755 if executable else 0o644)


def validate(binary: Path, debug: Path, go: Path) -> None:
    header, program, dynamic = (readelf(binary, x) for x in ("-h", "-lW", "-dW"))
    require("ELF64" in header and "Advanced Micro Devices X86-64" in header, "wrong ELF target")
    require(bool(re.search(r"Type:\s+DYN", header)), "binary is not PIE")
    require("GNU_RELRO" in program and "BIND_NOW" in dynamic, "missing full RELRO")
    require("PIE" in dynamic, "missing PIE dynamic flag")
    stack = [line for line in program.splitlines() if "GNU_STACK" in line]
    require(len(stack) == 1 and bool(re.search(r"\sRW\s", stack[0])), "executable stack")
    require(
        not re.search(r"\((NEEDED|RPATH|RUNPATH|TEXTREL)\)", dynamic), "unsafe dynamic dependency"
    )
    require("/lib64/ld-linux-x86-64.so.2" in program, "unexpected ELF interpreter")
    sections = readelf(binary, "-SW")
    require(".debug_info" not in sections and ".symtab" not in sections, "binary is not stripped")
    require(".debug_info" in readelf(debug, "-SW"), "missing detached DWARF")
    require(identity(binary) == identity(debug), "debug identity mismatch")
    require(debug.name in readelf(binary, "--string-dump=.gnu_debuglink"), "missing debug link")
    metadata = subprocess.check_output([str(go), "version", "-m", str(binary)], text=True)
    require("github.com/peios/loregd" in metadata, "missing module identity")
    require("modernc.org/sqlite\tv1.48.1" in metadata, "unexpected SQLite version")
    require(
        "github.com/peios/peios-go" in metadata and "libp-go" not in metadata, "wrong Peios SDK"
    )
    for file in (binary, debug):
        content = file.read_bytes()
        for forbidden in (str(Path.cwd().resolve()), "/home/jack/", "/tmp/loregd-"):
            require(forbidden.encode() not in content, "build path leaked into ELF")
    smoke = subprocess.run([str(binary.resolve())], capture_output=True, timeout=10, check=False)
    require(smoke.returncode == 1 and not smoke.stdout, "unexpected no-argument behavior")
    require(
        b"argument error: no hive arguments provided" in smoke.stderr, "missing argument diagnostic"
    )


def stage(manifest: dict, output: Path) -> None:
    output.mkdir(parents=True, exist_ok=True)
    sources = {item["relative"]: Path(item["path"]) for item in manifest["sources"]}
    binary = output / "usr/sbin/loregd"
    copy_file(Path(manifest["binary"]), binary, True)
    build_id = identity(binary)
    debug = output / f"debug/usr/lib/debug/.build-id/{build_id[:2]}/{build_id[2:]}.debug"
    debug.parent.mkdir(parents=True)
    subprocess.run(["objcopy", "--only-keep-debug", str(binary), str(debug)], check=True)
    subprocess.run(["strip", "--strip-unneeded", str(binary)], check=True)
    subprocess.run(["objcopy", "--add-gnu-debuglink=" + str(debug), str(binary)], check=True)
    man = output / "usr/share/man/man8/loregd.8.gz"
    man.parent.mkdir(parents=True)
    man.write_bytes(gzip.compress(sources["docs/man/loregd.8"].read_bytes(), mtime=0))
    state = output / "var/state/loregd"
    state.mkdir(parents=True)
    state.chmod(0o700)
    debugsource = output / "usr/src/debug/dev.peios.loregd"
    go_sources = Path(manifest["go_sources"]) / "src"
    fingerprints = {}
    for file in sorted(go_sources.rglob("*")):
        if file.is_file():
            relative = file.relative_to(go_sources)
            copy_file(file, debugsource / relative)
            fingerprints[str(relative)] = hashlib.sha256(file.read_bytes()).hexdigest()
    sdk_root = Path(manifest["sdk_root"])
    for name in manifest["sdk_sources"]:
        file = Path(name)
        relative = Path("GOROOT") / file.relative_to(sdk_root)
        copy_file(file, debugsource / relative)
        fingerprints[str(relative)] = hashlib.sha256(file.read_bytes()).hexdigest()
    for name, file in sources.items():
        if name.startswith("third_party/licenses/"):
            copy_file(file, output / "licenses" / name.removeprefix("third_party/licenses/"))
    require((output / "licenses/go/LICENSE").is_file(), "missing Go licence")
    origins = json.loads(sources["third_party/licenses/ORIGIN.json"].read_text())
    require(
        origins["go"]["version"] == manifest["go_version"], "Go licence/compiler version mismatch"
    )
    for module, record in origins.items():
        for name, digest in record["files"].items():
            file = output / "licenses" / module / name
            require(
                hashlib.sha256(file.read_bytes()).hexdigest() == digest, "licence digest mismatch"
            )
    module_text = subprocess.check_output(
        [str(Path(manifest["go"]).resolve()), "version", "-m", str(binary)], text=True
    )
    modules = {}
    previous = None
    for line in module_text.splitlines():
        fields = line.split()
        if fields and fields[0] == "dep":
            previous = fields[1]
            modules[previous] = fields[2]
        elif fields and fields[0] == "=>":
            require(previous == fields[1], "unexpected dependency replacement path")
            modules[previous] = fields[2]
    expected = {name: data["version"] for name, data in origins.items() if name != "go"}
    require(modules == expected, "embedded dependencies differ from packaged licence versions")
    validate(binary, debug, Path(manifest["go"]).resolve())
    metadata = {
        "environment": "peios-native" if manifest["native"] else "linux-development",
        "release_ready": False,
        "version": manifest["version"],
        "go_version": manifest["go_version"],
        "build_id": build_id,
        "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "source_files": fingerprints,
        "modules": {name: data["version"] for name, data in origins.items()},
        "debug_source_prefix": "/usr/src/debug/dev.peios.loregd",
    }
    (output / "build.json").write_text(json.dumps(metadata, sort_keys=True, indent=2) + "\n")


if __name__ == "__main__":
    stage(json.loads(Path(sys.argv[1]).read_text()), Path(sys.argv[2]))
