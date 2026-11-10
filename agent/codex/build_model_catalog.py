#!/usr/bin/env python3
"""Build a native Codex catalog from a runtime's bundled models and an upstream ModelsResponse."""
import argparse
import json
from pathlib import Path


def load_models(path):
    models = json.loads(path.read_text())["models"]
    by_slug = {row["slug"]: row for row in models}
    if not by_slug or len(by_slug) != len(models):
        raise ValueError(f"{path}: expected nonempty, unique model slugs")
    return by_slug


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for arg in ("bundled", "source", "aliases", "output"):
        parser.add_argument("--" + arg, type=Path, required=True)
    args = parser.parse_args()
    models = load_models(args.bundled)
    upstream = load_models(args.source)
    aliases = json.loads(args.aliases.read_text())
    for alias, canonical in aliases.items():
        if not alias or canonical not in upstream:
            raise ValueError(f"Missing Codex descriptor for {alias!r}: {canonical!r}")
        if alias in models and alias != canonical:
            raise ValueError(f"Alias {alias!r} would overwrite an existing runtime model")
        models[alias] = {**upstream[canonical], "slug": alias}
    # Only descriptors are exported. Account identity, auth and cache metadata stay at source.
    payload = json.dumps({"models": list(models.values())}, ensure_ascii=False)
    with args.output.open("x", encoding="utf-8") as stream:
        stream.write(payload + "\n")


if __name__ == "__main__":
    main()
