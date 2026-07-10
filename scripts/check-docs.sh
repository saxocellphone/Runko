#!/bin/bash
# docs-check (§14.5.7): the content-tier gate for prose changes. Verifies
# that every relative markdown link in every git-tracked *.md file points at
# a path that exists in the working tree - the exact rot class a doc-only
# change can introduce and nothing else would catch (code checks don't read
# prose; this repo's own cleanup found several such stale links). Fast by
# design: no toolchain beyond git + python3, so a doc edit's entire required
# check set runs in seconds. A REAL check, not policy theater - it exists so
# default-deny (§13.5) holds for prose changes without conscripting the
# build graph.
set -euo pipefail
cd "$(dirname "$0")/.."

git ls-files -z -- '*.md' | python3 -c '
import os, re, sys

link = re.compile(r"\[[^\]]*\]\(([^)\s]+)(?:\s+\"[^\"]*\")?\)")
fence = re.compile(r"^\s*(```|~~~)")

broken = []
files = sys.stdin.buffer.read().split(b"\0")
for name in files:
    if not name:
        continue
    name = name.decode()
    in_fence = False
    with open(name, encoding="utf-8") as f:
        for lineno, line in enumerate(f, 1):
            if fence.match(line):
                in_fence = not in_fence
                continue
            if in_fence:
                continue
            for m in link.finditer(line):
                target = m.group(1)
                if re.match(r"^[a-z][a-z0-9+.-]*:", target):  # http:, https:, mailto:
                    continue
                target = target.split("#", 1)[0]
                if not target:  # pure #anchor
                    continue
                resolved = os.path.normpath(os.path.join(os.path.dirname(name), target))
                if not os.path.exists(resolved):
                    broken.append(f"{name}:{lineno}: broken link -> {m.group(1)}")

if broken:
    print("\n".join(broken))
    print(f"\ncheck-docs: {len(broken)} broken relative link(s)", file=sys.stderr)
    sys.exit(1)
print("check-docs: all relative markdown links resolve")
'
