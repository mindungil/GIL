"""Compile gil's .proto files into Python gRPC stubs.

Usage::

    python -m gil_swebench.compile_protos                       # default paths
    python -m gil_swebench.compile_protos --proto-root /path/to/gil/proto

Writes ``*_pb2.py`` and ``*_pb2_grpc.py`` into ``gil_swebench/proto/``.
Generated files are not committed -- ``.gitignore`` excludes them. Run
after a ``pip install -e .`` or whenever the .proto files change.

This is a parallel of ``gil_atropos.compile_protos``; we keep separate
copies so each adapter package is independently installable.
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path


def _default_proto_root() -> Path:
    """Best-effort guess for where gil's proto files live.

    Works for in-tree dev: ``<gil-repo>/python/gil_swebench/`` -> ``../../proto``.
    Installed wheels should pass ``--proto-root`` explicitly.
    """
    here = Path(__file__).resolve().parent  # .../gil_swebench
    return here.parent.parent / "proto"     # .../gil/proto


def _output_dir() -> Path:
    return Path(__file__).resolve().parent / "proto"


def _find_proto_files(proto_root: Path) -> list[Path]:
    pattern_dir = proto_root / "gil" / "v1"
    if not pattern_dir.is_dir():
        raise FileNotFoundError(
            f"Expected proto files at {pattern_dir} but the directory does not exist. "
            "Pass --proto-root to point at gil's proto/ directory."
        )
    files = sorted(pattern_dir.glob("*.proto"))
    if not files:
        raise FileNotFoundError(f"No .proto files found in {pattern_dir}")
    return files


def compile_protos(proto_root: Path, out_dir: Path) -> int:
    """Invoke grpc_tools.protoc. Returns the exit code."""
    try:
        from grpc_tools import protoc  # type: ignore[import-not-found]
    except ImportError as exc:
        print(
            "ERROR: grpcio-tools is not installed. Install it with:\n"
            "    pip install grpcio-tools\n",
            file=sys.stderr,
        )
        raise SystemExit(2) from exc

    proto_files = _find_proto_files(proto_root)
    out_dir.mkdir(parents=True, exist_ok=True)

    argv = [
        "protoc",
        f"--proto_path={proto_root}",
        f"--python_out={out_dir}",
        f"--grpc_python_out={out_dir}",
    ]

    extra_includes = _googleapis_include_paths()
    for inc in extra_includes:
        argv.append(f"--proto_path={inc}")

    argv.extend(str(p.relative_to(proto_root)) for p in proto_files)

    print("Running:", " ".join(argv))
    rc = protoc.main(argv)
    if rc != 0:
        print(f"protoc exited with code {rc}", file=sys.stderr)
        return rc

    print(f"Generated stubs into {out_dir}:")
    for f in sorted(out_dir.glob("*.py")):
        if f.name == "__init__.py":
            continue
        print(f"  - {f.name}")
    return 0


def _googleapis_include_paths() -> list[Path]:
    """Look for googleapis .proto files bundled with grpc_tools or system."""
    paths: list[Path] = []
    candidates = [
        Path("/usr/include"),
        Path("/usr/local/include"),
        Path.home() / ".local" / "include",
    ]
    for c in candidates:
        if (c / "google" / "api" / "annotations.proto").is_file():
            paths.append(c)

    try:
        import grpc_tools  # type: ignore[import-not-found]

        proto_inc = Path(grpc_tools.__file__).parent / "_proto"
        if proto_inc.is_dir():
            paths.append(proto_inc)
    except ImportError:
        pass

    return paths


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    parser.add_argument(
        "--proto-root",
        type=Path,
        default=_default_proto_root(),
        help="Path to gil's proto/ root (default: %(default)s).",
    )
    parser.add_argument(
        "--out-dir",
        type=Path,
        default=_output_dir(),
        help="Where to write generated stubs (default: %(default)s).",
    )
    parser.add_argument(
        "--clean",
        action="store_true",
        help="Remove existing *_pb2*.py before regenerating.",
    )
    args = parser.parse_args(argv)

    if args.clean and args.out_dir.is_dir():
        for f in args.out_dir.glob("*_pb2*.py"):
            print(f"Removing {f}")
            f.unlink()

    return compile_protos(args.proto_root, args.out_dir)


if __name__ == "__main__":
    raise SystemExit(main())
