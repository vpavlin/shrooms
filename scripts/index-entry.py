#!/usr/bin/env python3
"""Write the entry a Basecamp repository index needs to list this module.

Basecamp installs modules from a repository — a `logos-repo.json` pointing at
an index that lists each package, its download URL, its size and its hashes
(see https://apps.vpavlin.xyz/logos-repo.json). Shrooms publishes its own
release rather than living in somebody else's repository, so what is needed to
be listed there one day is exactly this object, with a URL that already exists.

Generated rather than written by hand because three of its five fields are
derived from the file — size, sha256 and the manifest's root hash — and a
hand-copied hash is wrong the first time somebody rebuilds.

    make basecamp-publish     # builds, releases, and writes this
"""
import hashlib
import json
import pathlib
import subprocess
import sys


def main():
    root = pathlib.Path(__file__).resolve().parent.parent

    # A path may be given, because there are two packages — the view and the
    # core module it depends on — and "whatever is in result/" is whichever was
    # built last.
    if len(sys.argv) > 1:
        lgx = pathlib.Path(sys.argv[1]).resolve()
    else:
        found = sorted((root / "result").glob("*.lgx"))
        if not found:
            sys.exit("no .lgx in result/ — run `make basecamp-lgx` first")
        lgx = pathlib.Path(found[0].resolve())
    raw = lgx.read_bytes()
    manifest = json.loads(subprocess.run(
        ["tar", "xzOf", str(lgx), "manifest.json"],
        capture_output=True, text=True, check=True).stdout)

    name, version = manifest["name"], manifest["version"]
    entry = {
        "name": name,
        "versions": [{
            "publisherRef": f"{name}-v{version}",
            "url": (f"https://github.com/vpavlin/shrooms/releases/download/"
                    f"{name}-v{version}/{name}-{version}.lgx"),
            "size": len(raw),
            "sha256": hashlib.sha256(raw).hexdigest(),
            "rootHash": manifest.get("hashes", {}).get("root", ""),
            "manifest": manifest,
        }],
    }

    out = root / f"basecamp/index-entry-{name}.json"
    out.write_text(json.dumps(entry, indent=2) + "\n")
    print(f"wrote {out.relative_to(root)} for {name} {version}")


if __name__ == "__main__":
    main()
