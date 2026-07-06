#!/usr/bin/env python3
"""Match PR changed files against CODEOWNERS and emit triage metadata."""

from __future__ import annotations

import argparse
import fnmatch
import json
import os
import sys
import urllib.error
import urllib.request

COMMENT_MARKER = "<!-- pr-module-triage -->"

MODULE_LABELS: dict[str, str] = {
    "/CubeNet/": "CubeNet (network)",
    "/CubeEgress/": "CubeEgress (egress)",
    "/network-agent/": "network-agent",
    "/Cubelet/": "Cubelet (orchestration)",
    "/CubeMaster/": "CubeMaster (control plane)",
    "/CubeShim/": "CubeShim (containerd shim)",
    "/agent/": "agent (in-guest runtime)",
    "/hypervisor/": "hypervisor",
    "/CubeAPI/": "CubeAPI",
    "/sdk/": "SDK",
    "/docs/": "docs",
    "/deploy/one-click/terraform/": "deploy/terraform",
    "/deploy/one-click/": "deploy/one-click",
    "/.github/workflows/": "CI/workflows",
    "/examples/": "examples",
    "/cubecow": "cubecow",
    "*aarch64*": "ARM (aarch64)",
    "*arm64*": "ARM (arm64)",
    "*": "default (catch-all)",
}


def parse_codeowners(path: str) -> list[tuple[str, list[str]]]:
    rules: list[tuple[str, list[str]]] = []
    with open(path, encoding="utf-8") as handle:
        for raw in handle:
            line = raw.split("#", 1)[0].strip()
            if not line:
                continue
            parts = line.split()
            if len(parts) < 2:
                continue
            pattern = parts[0]
            owners = [part[1:] for part in parts[1:] if part.startswith("@")]
            if owners:
                rules.append((pattern, owners))
    return rules


def normalize_pattern(pattern: str) -> str:
    return pattern[1:] if pattern.startswith("/") else pattern


def matches_path(file_path: str, pattern: str) -> bool:
    if pattern == "*":
        return True

    normalized = normalize_pattern(pattern)
    if normalized.endswith("/"):
        prefix = normalized
        return file_path.startswith(prefix) or file_path == prefix.rstrip("/")

    if "/" not in normalized and not normalized.startswith("*"):
        return (
            file_path == normalized
            or file_path.endswith("/" + normalized)
            or fnmatch.fnmatchcase(file_path, normalized)
            or fnmatch.fnmatchcase(file_path, "*/" + normalized)
        )

    return fnmatch.fnmatchcase(file_path, normalized)


def owners_for_file(file_path: str, rules: list[tuple[str, list[str]]]) -> list[str]:
    owners: list[str] = []
    for pattern, rule_owners in rules:
        if matches_path(file_path, pattern):
            owners = rule_owners
    return owners


def module_for_file(file_path: str, rules: list[tuple[str, list[str]]]) -> str:
    matched_pattern = "*"
    for pattern, _ in rules:
        if matches_path(file_path, pattern):
            matched_pattern = pattern
    return MODULE_LABELS.get(matched_pattern, matched_pattern)


def github_request(url: str, token: str) -> object:
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "application/vnd.github+json",
            "Authorization": f"Bearer {token}",
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": "cubesandbox-pr-module-triage",
        },
    )
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)


def list_pr_files(owner: str, repo: str, pr_number: int, token: str) -> list[str]:
    files: list[str] = []
    page = 1
    while True:
        url = (
            f"https://api.github.com/repos/{owner}/{repo}/pulls/{pr_number}/files"
            f"?per_page=100&page={page}"
        )
        batch = github_request(url, token)
        if not isinstance(batch, list) or not batch:
            break
        files.extend(item["filename"] for item in batch if item.get("filename"))
        if len(batch) < 100:
            break
        page += 1
    return files


def build_summary(
    changed_files: list[str],
    rules: list[tuple[str, list[str]]],
) -> tuple[dict[str, list[str]], dict[str, set[str]], set[str]]:
    modules: dict[str, list[str]] = {}
    module_owners: dict[str, set[str]] = {}
    all_owners: set[str] = set()

    for file_path in changed_files:
        module = module_for_file(file_path, rules)
        modules.setdefault(module, []).append(file_path)
        owners = owners_for_file(file_path, rules)
        module_owners.setdefault(module, set()).update(owners)
        all_owners.update(owners)

    return modules, module_owners, all_owners


def render_comment(modules: dict[str, list[str]], module_owners: dict[str, set[str]]) -> str:
    lines = [
        COMMENT_MARKER,
        "## PR module triage",
        "",
        "This comment was posted automatically from `.github/CODEOWNERS`.",
        "",
        "| Module | Changed files | Suggested owners |",
        "| --- | ---: | --- |",
    ]

    for module in sorted(modules):
        owners = ", ".join(f"@{owner}" for owner in sorted(module_owners.get(module, set())))
        lines.append(f"| {module} | {len(modules[module])} | {owners or '-'} |")

    lines.extend(
        [
            "",
            "<details>",
            "<summary>Changed file paths</summary>",
            "",
        ]
    )
    for file_path in sorted({path for paths in modules.values() for path in paths}):
        lines.append(f"- `{file_path}`")
    lines.extend(["", "</details>", ""])
    return "\n".join(lines)


def write_github_output(key: str, value: str) -> None:
    output_path = os.environ.get("GITHUB_OUTPUT")
    if not output_path:
        return
    with open(output_path, "a", encoding="utf-8") as handle:
        handle.write(f"{key}={value}\n")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--codeowners", required=True)
    parser.add_argument("--pr-number", type=int, required=True)
    parser.add_argument("--owner", required=True)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--author", default="")
    parser.add_argument("--comment-file", default="")
    parser.add_argument("--emit-github-output", action="store_true")
    args = parser.parse_args()

    token = os.environ.get("GITHUB_TOKEN")
    if not token:
        print("GITHUB_TOKEN is required", file=sys.stderr)
        return 1

    rules = parse_codeowners(args.codeowners)
    changed_files = list_pr_files(args.owner, args.repo, args.pr_number, token)
    modules, module_owners, all_owners = build_summary(changed_files, rules)

    if args.author:
        all_owners.discard(args.author)

    payload = {
        "changed_files": changed_files,
        "modules": {module: sorted(paths) for module, paths in modules.items()},
        "module_owners": {
            module: sorted(owners) for module, owners in module_owners.items()
        },
        "owners": sorted(all_owners),
        "comment": render_comment(modules, module_owners),
    }

    print(json.dumps(payload, indent=2))

    if args.comment_file:
        with open(args.comment_file, "w", encoding="utf-8") as handle:
            handle.write(payload["comment"])

    if args.emit_github_output:
        write_github_output("owners", ",".join(payload["owners"]))
        write_github_output("modules", ",".join(sorted(modules.keys())))

    return 0


if __name__ == "__main__":
    raise SystemExit(main())
