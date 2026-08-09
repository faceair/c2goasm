#!/bin/bash
# Download and cache one of the complete upstream E2E source corpora.
set -euo pipefail

if [ "$#" -ne 1 ] || { [ "$1" != pcre2 ] && [ "$1" != quickjs ] && [ "$1" != sqlite ]; }; then
    echo "usage: scripts/fetch-test-source.sh pcre2|quickjs|sqlite" >&2
    exit 2
fi

python3 - "$1" <<'PY'
import contextlib
import fcntl
import hashlib
import os
from pathlib import Path, PurePosixPath
import shutil
import sys
import tarfile
import tempfile
import urllib.request
import zipfile

SOURCES = {
    "quickjs": {
        "url": "https://bellard.org/quickjs/quickjs-2026-06-04.tar.xz",
        "sha256": "b376e839b322978313d929fd20663b11ba58b75df5a46c126dd19ea2fa70ad2a",
        "archive": "tar",
        "root": "quickjs-2026-06-04",
        "required": ("VERSION", "quickjs.c", "cutils.c"),
        "version": b"2026-06-04",
    },
    "pcre2": {
        "url": "https://github.com/PCRE2Project/pcre2/releases/download/pcre2-10.47/pcre2-10.47.tar.gz",
        "sha256": "c08ae2388ef333e8403e670ad70c0a11f1eed021fd88308d7e02f596fcd9dc16",
        "archive": "tar",
        "root": "pcre2-10.47",
        "required": (
            "configure.ac",
            "src/config.h.generic",
            "src/pcre2.h.generic",
            "src/pcre2_chartables.c.dist",
            "src/pcre2_auto_possess.c",
            "src/pcre2_chkdint.c",
            "src/pcre2_compile.c",
            "src/pcre2_compile_cgroup.c",
            "src/pcre2_compile_class.c",
            "src/pcre2_config.c",
            "src/pcre2_context.c",
            "src/pcre2_convert.c",
            "src/pcre2_dfa_match.c",
            "src/pcre2_error.c",
            "src/pcre2_extuni.c",
            "src/pcre2_find_bracket.c",
            "src/pcre2_jit_compile.c",
            "src/pcre2_maketables.c",
            "src/pcre2_match.c",
            "src/pcre2_match_data.c",
            "src/pcre2_match_next.c",
            "src/pcre2_newline.c",
            "src/pcre2_ord2utf.c",
            "src/pcre2_pattern_info.c",
            "src/pcre2_script_run.c",
            "src/pcre2_serialize.c",
            "src/pcre2_string_utils.c",
            "src/pcre2_study.c",
            "src/pcre2_substitute.c",
            "src/pcre2_substring.c",
            "src/pcre2_tables.c",
            "src/pcre2_ucd.c",
            "src/pcre2_valid_utf.c",
            "src/pcre2_xclass.c",
        ),
        "version": b"m4_define(pcre2_minor, [47])",
    },
    "sqlite": {
        "url": "https://www.sqlite.org/2025/sqlite-amalgamation-3480000.zip",
        "sha256": "d9a15a42db7c78f88fe3d3c5945acce2f4bfe9e4da9f685cd19f6ea1d40aa884",
        "archive": "zip",
        "root": "sqlite-amalgamation-3480000",
        "required": ("sqlite3.c", "sqlite3.h"),
        "version": b'#define SQLITE_VERSION        "3.48.0"',
    },
}

kind = sys.argv[1]
spec = SOURCES[kind]
cache_base = os.environ.get("C2GOASM_CACHE_DIR")
if not cache_base:
    cache_base = os.environ.get("XDG_CACHE_HOME")
    if cache_base:
        cache_base = str(Path(cache_base).expanduser() / "c2goasm")
    else:
        cache_base = str(Path.home() / ".cache" / "c2goasm")
cache_root = (Path(cache_base).expanduser() / "sources").resolve()
cache_root.mkdir(parents=True, exist_ok=True)
installed = cache_root / spec["root"]
marker = installed / ".c2goasm-source.ok"
lock_path = cache_root / f".{kind}.lock"


def status(message):
    print(message, file=sys.stderr, flush=True)


def digest_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def manifest_entries(directory):
    entries = []
    for path in sorted(directory.rglob("*")):
        if not path.is_file() or path.is_symlink():
            continue
        relative = path.relative_to(directory).as_posix()
        if relative in {marker.name, f"{marker.name}.tmp", ".c2goasm-source.manifest", ".c2goasm-source.manifest.tmp"}:
            continue
        entries.append((relative, digest_file(path)))
    return entries


def verify_manifest(directory):
    manifest = directory / ".c2goasm-source.manifest"
    expected = {}
    for line in manifest.read_text(encoding="ascii").splitlines():
        digest, separator, relative = line.partition("  ")
        if separator != "  " or len(digest) != 64 or any(char not in "0123456789abcdef" for char in digest):
            return False
        path = PurePosixPath(relative)
        if path.is_absolute() or not path.parts or ".." in path.parts or relative in expected:
            return False
        expected[relative] = digest
    actual = dict(manifest_entries(directory))
    return expected == actual and all(actual[name] == digest for name, digest in expected.items())


def complete():
    if not installed.is_dir() or installed.is_symlink() or not marker.is_file():
        return False
    try:
        if marker.read_text(encoding="ascii").strip() != spec["sha256"]:
            return False
        if not verify_manifest(installed):
            return False
        validate_source(installed)
        return True
    except (OSError, UnicodeError, RuntimeError):
        return False


@contextlib.contextmanager
def cache_lock():
    with lock_path.open("a+") as lock:
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(lock.fileno(), fcntl.LOCK_UN)


def safe_member_path(destination, name):
    if "\x00" in name:
        raise RuntimeError("archive member contains NUL")
    path = PurePosixPath(name)
    if path.is_absolute() or not path.parts or ".." in path.parts:
        raise RuntimeError(f"unsafe archive member path: {name!r}")
    if path.parts[0] != spec["root"]:
        raise RuntimeError(f"unexpected archive root: {name!r}")
    result = (destination / Path(*path.parts)).resolve()
    destination_resolved = destination.resolve()
    if result != destination_resolved and destination_resolved not in result.parents:
        raise RuntimeError(f"archive member escapes extraction root: {name!r}")
    return result, path


def extract_tar(archive, destination):
    with tarfile.open(archive, mode="r:*") as source:
        seen = set()
        for member in source.getmembers():
            target, relative = safe_member_path(destination, member.name)
            key = str(relative)
            if key in seen:
                raise RuntimeError(f"duplicate archive member: {member.name!r}")
            seen.add(key)
            if member.issym() or member.islnk() or not (member.isdir() or member.isreg()):
                raise RuntimeError(f"unsupported archive member type: {member.name!r}")
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            extracted = source.extractfile(member)
            if extracted is None:
                raise RuntimeError(f"cannot read archive member: {member.name!r}")
            with extracted, target.open("xb") as output:
                shutil.copyfileobj(extracted, output)


def extract_zip(archive, destination):
    with zipfile.ZipFile(archive) as source:
        seen = set()
        for member in source.infolist():
            target, relative = safe_member_path(destination, member.filename)
            key = str(relative)
            if key in seen:
                raise RuntimeError(f"duplicate archive member: {member.filename!r}")
            seen.add(key)
            mode = (member.external_attr >> 16) & 0o170000
            is_directory = member.filename.endswith("/") or mode == 0o040000
            if mode and mode not in (0o100000, 0o040000):
                raise RuntimeError(f"unsupported archive member type: {member.filename!r}")
            if is_directory:
                target.mkdir(parents=True, exist_ok=True)
                continue
            target.parent.mkdir(parents=True, exist_ok=True)
            with source.open(member) as extracted, target.open("xb") as output:
                shutil.copyfileobj(extracted, output)


def validate_source(directory):
    if not directory.is_dir() or directory.is_symlink():
        raise RuntimeError("downloaded archive has no expected source root")
    for relative in spec["required"]:
        path = directory / relative
        if not path.is_file() or path.is_symlink():
            raise RuntimeError(f"downloaded source is missing {relative}")
    if kind == "sqlite":
        if spec["version"] not in (directory / "sqlite3.c").read_bytes():
            raise RuntimeError("downloaded source has an unexpected upstream version")
    elif kind == "pcre2":
        if spec["version"] not in (directory / "configure.ac").read_bytes():
            raise RuntimeError("downloaded source has an unexpected upstream version")
    elif (directory / "VERSION").read_bytes().strip() != spec["version"]:
        raise RuntimeError("downloaded source has an unexpected upstream version")


def download(archive):
    status(f"downloading {spec['url']}")
    digest = hashlib.sha256()
    request = urllib.request.Request(spec["url"], headers={"User-Agent": "c2goasm-test-source/1"})
    with urllib.request.urlopen(request, timeout=120) as response, archive.open("wb") as output:
        while True:
            chunk = response.read(1024 * 1024)
            if not chunk:
                break
            digest.update(chunk)
            output.write(chunk)
    actual = digest.hexdigest()
    if actual != spec["sha256"]:
        raise RuntimeError(f"SHA-256 mismatch: got {actual}, want {spec['sha256']}")


with cache_lock():
    if complete():
        status(f"using cached {kind} source: {installed}")
    else:
        archive_fd, archive_name = tempfile.mkstemp(
            prefix=f".{kind}-", suffix=".download", dir=cache_root
        )
        os.close(archive_fd)
        archive = Path(archive_name)
        staging = Path(tempfile.mkdtemp(prefix=f".{kind}-", suffix=".staging", dir=cache_root))
        try:
            download(archive)
            if spec["archive"] == "tar":
                extract_tar(archive, staging)
            else:
                extract_zip(archive, staging)
            staged_root = staging / spec["root"]
            validate_source(staged_root)
            manifest_tmp = staged_root / ".c2goasm-source.manifest.tmp"
            manifest_tmp.write_text(
                "".join(f"{digest}  {relative}\n" for relative, digest in manifest_entries(staged_root)),
                encoding="ascii",
            )
            os.replace(manifest_tmp, staged_root / ".c2goasm-source.manifest")
            marker_tmp = staged_root / ".c2goasm-source.ok.tmp"
            marker_tmp.write_text(spec["sha256"] + "\n", encoding="ascii")
            os.replace(marker_tmp, staged_root / marker.name)
            if installed.exists() or installed.is_symlink():
                if installed.is_dir() and not installed.is_symlink():
                    shutil.rmtree(installed)
                else:
                    installed.unlink()
            os.replace(staged_root, installed)
            status(f"installed {kind} source cache: {installed}")
        finally:
            archive.unlink(missing_ok=True)
            shutil.rmtree(staging, ignore_errors=True)

    print(installed)
PY
