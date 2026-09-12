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
import subprocess
import sys
import urllib.request

CORPUS = pathlib.Path(__file__).resolve().parent.parent / "testdata" / "corpus"


def digest(data: bytes) -> str:
    return "sha256:" + hashlib.sha256(data).hexdigest()


def fetch_repo(spec: dict) -> int:
    """Check out an exploded specification at a pinned commit.

    A directory has no single digest, so the commit is the pin. The clone is
    shallow and fetches exactly that commit: the corpus is evidence, not a
    mirror of someone's default branch.
    """
    target = CORPUS / spec["name"]
    if (target / ".git").exists():
        head = run(["git", "-C", str(target), "rev-parse", "HEAD"])
        if head == spec["commit"]:
            print(f"ok       {spec['name']} (cached at {head[:12]})")
            return 0
        print(f"stale    {spec['name']}: {head[:12]} != pinned {spec['commit'][:12]}", file=sys.stderr)

    print(f"cloning  {spec['name']} <- {spec['repo']} @ {spec['commit'][:12]}")
    try:
        target.mkdir(parents=True, exist_ok=True)
        run(["git", "init", "--quiet", str(target)])
        run(["git", "-C", str(target), "remote", "remove", "origin"], check=False)
        run(["git", "-C", str(target), "remote", "add", "origin", spec["repo"]])
        run(["git", "-C", str(target), "fetch", "--quiet", "--depth", "1", "origin", spec["commit"]])
        run(["git", "-C", str(target), "checkout", "--quiet", "FETCH_HEAD"])
    except subprocess.CalledProcessError as err:
        print(f"FAILED   {spec['name']}: {err}", file=sys.stderr)
        return 1

    if not (target / spec["spec"]).exists():
        print(f"FAILED   {spec['name']}: {spec['spec']} missing from the checkout", file=sys.stderr)
        return 1
    print(f"ok       {spec['name']}")
    return 0


def run(args: list[str], check: bool = True) -> str:
    result = subprocess.run(args, capture_output=True, text=True, check=check)
    return result.stdout.strip()


def main() -> int:
    manifest = json.loads((CORPUS / "MANIFEST.json").read_text())
    failures = 0

    for spec in manifest["specs"]:
        if "repo" in spec:
            failures += fetch_repo(spec)
            continue

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
