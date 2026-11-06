#!/usr/bin/env python3
"""Writes THIRD_PARTY_NOTICES: the licence text of every third-party package that ends up in the app (Go modules linked into the
binary, npm packages bundled into the UI). Their licences ask for their notices to travel with the software; the app's own code is
under the Unlicense (LICENSE). Run it from the repository root after dependencies change:  python3 scripts/notices.py"""
import json, os, subprocess, sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NAMES = ("LICENSE", "LICENSE.txt", "LICENSE.md", "COPYING", "LICENCE", "License", "license")


def run(cmd, cwd):
    return subprocess.run(cmd, cwd=cwd, check=True, capture_output=True, text=True).stdout


def licence_text(directory):
    for n in NAMES:
        p = os.path.join(directory, n)
        if os.path.isfile(p):
            return open(p, encoding="utf-8", errors="replace").read().strip()
    return None


entries = []

# Go: modules that provide a package linked into cmd/server (the standard library and this module excluded)
out = run(["go", "list", "-deps", "-f", "{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}{{end}}{{end}}", "./cmd/server"], ROOT)
for line in sorted(set(out.splitlines())):
    if line:
        path, version, directory = line.split("\t")
        entries.append(("Go", path, version, directory))

# npm: the packages that Vite bundles into the UI (see web/dist). Build tools (vite, the Vue compiler, typescript, ...) are not shipped.
# Review this list when the UI's dependencies change.
SHIPPED = ["vue", "@vue/shared", "@vue/reactivity", "@vue/runtime-core", "@vue/runtime-dom", "vue-router", "@vue/devtools-api"]
web = os.path.join(ROOT, "web")
for name in SHIPPED:
    directory = os.path.join(web, "node_modules", *name.split("/"))
    meta = json.load(open(os.path.join(directory, "package.json")))
    entries.append(("npm", name, meta["version"], directory))

MIT = """MIT License

Copyright (c) {who}

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files
(the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge,
publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do
so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE
FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION
WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE."""


def fallback_text(directory):
    """Some packages declare their licence in package.json but ship no licence file."""
    try:
        meta = json.load(open(os.path.join(directory, "package.json")))
    except OSError:
        return None
    author = meta.get("author")
    who = author.get("name") if isinstance(author, dict) else author
    if meta.get("license") == "MIT" and who:
        return MIT.format(who=who) + "\n\n(The package declares the MIT licence in its package.json and ships no licence file.)"
    return None


missing = []
parts = [
    "THIRD-PARTY NOTICES\n\nThis software includes the following third-party packages. Each is distributed under its own licence, reproduced below.\n"
    "The software itself is released under the Unlicense (see LICENSE). The Docker image also contains iptv-org/epg (in /opt/epg) and its\n"
    "dependencies, each with its own licence file there.\n"
]
for kind, name, version, directory in sorted(entries, key=lambda e: (e[0], e[1])):
    text = licence_text(directory) or fallback_text(directory)
    if text is None:
        missing.append(name)
        continue
    parts.append(f"\n{'=' * 78}\n{name} {version} ({kind})\n{'=' * 78}\n\n{text}\n")
open(os.path.join(ROOT, "THIRD_PARTY_NOTICES"), "w", encoding="utf-8").write("".join(parts))
print(f"{len(entries) - len(missing)} packages written to THIRD_PARTY_NOTICES")
if missing:
    print("no licence file found for:", ", ".join(missing), file=sys.stderr)
    sys.exit(1)
