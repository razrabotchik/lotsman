#!/usr/bin/env python3
"""Fetch the vendor corpus pinned by testdata/corpus/MANIFEST.json.

Each document is downloaded from an immutable ref and checked against the
digest recorded in the manifest. A mismatch is a hard failure: the corpus
numbers in docs/corpus.md only mean something if everyone measures the same
bytes.
"""

import hashlib
import json
import pathlib
import sys
import urllib.request

CORPUS = pathlib.Path(__file__).resolve().parent.parent / "testdata" / "corpus"


def digest(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def main() -> int:
    manifest = json.loads((CORPUS / "MANIFEST.json").read_text())
    failures = 0

    for spec in manifest["specs"]:
        target = CORPUS / spec["name"]
        if target.exists() and digest(target.read_bytes()) == spec["digest"]:
            print(f"ok       {spec['name']} (cached)")
            continue

        print(f"fetching {spec['name']} <- {spec['url']}")
        try:
            with urllib.request.urlopen(spec["url"], timeout=120) as response:
                data = response.read()
        except Exception as err:  # noqa: BLE001 - report and continue to the next spec
            print(f"FAILED   {spec['name']}: {err}", file=sys.stderr)
            failures += 1
            continue

        got = digest(data)
        if got != spec["digest"]:
            print(
                f"FAILED   {spec['name']}: digest {got} != pinned {spec['digest']}",
                file=sys.stderr,
            )
            failures += 1
            continue

        target.write_bytes(data)
        print(f"ok       {spec['name']} ({len(data)} bytes)")

    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
