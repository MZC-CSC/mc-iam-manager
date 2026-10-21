#!/usr/bin/env bash
# Sync docs/wiki/*.md to the GitHub Wiki repository.
# Usage: scripts/sync-wiki.sh [--apply] [--repo <owner/name>]
set -euo pipefail

APPLY=false
REPO="m-cmp/mc-iam-manager"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --apply) APPLY=true; shift ;;
    --repo) REPO="$2"; shift 2 ;;
    -h|--help) sed -n '2,3p' "$0"; exit 0 ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC="$ROOT/docs/wiki"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

git clone -q "https://github.com/${REPO}.wiki.git" "$WORK/wiki"
rsync -a --delete --exclude '.git' --exclude 'README.md' --include '*.md' --exclude '*' "$SRC/" "$WORK/wiki/"

cd "$WORK/wiki"
git add -A
if git diff --cached --quiet; then
  echo "Wiki is already up to date."
  exit 0
fi
git diff --cached --stat

if [[ "$APPLY" != true ]]; then
  echo
  echo "Dry run only. Re-run with --apply to push."
  exit 0
fi

SHA="$(git -C "$ROOT" rev-parse --short HEAD)"
git commit -q -m "Sync wiki from docs/wiki (${SHA})"
git push -q origin HEAD
echo "Wiki updated from ${SHA}."
