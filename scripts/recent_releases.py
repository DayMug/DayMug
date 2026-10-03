#!/usr/bin/env python3
"""Print the manifest's recent_releases JSON from the repository's v*.*.* tags.

The upgrade panel lists the 5 newest releases plus the newest release of each
of the 5 newest minor lines, newest first. With v1.1.0 … v1.5.5 tagged that is
v1.5.5 … v1.5.1, then v1.4.<max>, v1.3.<max>, v1.2.<max>, v1.1.<max>.

Each entry carries the commit subjects between it and the tag just below it,
minus chore:/docs:/ci: commits — the frontend renders a "general improvements"
fallback when that leaves nothing. Run from inside the repository.
"""

import json
import re
import subprocess
import sys

RECENT_COUNT = 5
MINOR_LINE_COUNT = 5
FILTER_PREFIXES = ("chore:", "docs:", "ci:")
TAG_RE = re.compile(r"^v(\d+)\.(\d+)\.(\d+)$")


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def all_tags():
    """Every vX.Y.Z tag, newest first. Pre-release and odd tags are skipped."""
    raw = run("git", "tag", "--list", "v*.*.*")
    parsed = []
    for t in raw.splitlines():
        m = TAG_RE.match(t.strip())
        if m:
            parsed.append((tuple(int(x) for x in m.groups()), t.strip()))
    parsed.sort(reverse=True)
    return [t for _, t in parsed], [v for v, _ in parsed]


def select(versions):
    """Indexes (into the newest-first list) of the releases to list."""
    chosen = set(range(min(len(versions), RECENT_COUNT)))
    seen_lines = []
    for i, v in enumerate(versions):
        line = v[:2]
        if line in seen_lines:
            continue
        seen_lines.append(line)
        chosen.add(i)
        if len(seen_lines) == MINOR_LINE_COUNT:
            break
    return sorted(chosen)


def notes(prev, current):
    if not prev:
        # The very first tag: don't dump the whole history.
        return []
    out = subprocess.check_output(
        ["git", "log", f"{prev}..{current}", "--no-merges", "--pretty=%h%x09%s"],
        text=True,
    )
    items = []
    for line in out.splitlines():
        h, _, s = line.partition("\t")
        s = s.strip()
        if not s or s.lower().startswith(FILTER_PREFIXES):
            continue
        items.append({"hash": h, "subject": s})
    return items


def main():
    tags, versions = all_tags()
    releases = []
    for i in select(versions):
        tag = tags[i]
        prev = tags[i + 1] if i + 1 < len(tags) else ""
        try:
            releases.append({
                "version": tag,
                # %aI is strict ISO-8601 with timezone.
                "released_at": run("git", "log", "-1", "--format=%aI", tag),
                "notes": notes(prev, tag),
            })
        except subprocess.CalledProcessError as e:
            # A tag unreachable in this clone shortens the list instead of
            # failing the release.
            print(f"warn: skipping {tag}: {e}", file=sys.stderr, flush=True)
    print(json.dumps(releases, ensure_ascii=False))


if __name__ == "__main__":
    main()
