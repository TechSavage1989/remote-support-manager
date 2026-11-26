#!/usr/bin/env bash
# Negative-control test for check-root.sh. It works on throwaway copies and
# never adds clutter to the real tree.
#  1. The clean tree passes (git mode when .git exists).
#  2. A copy without .git (like an extracted archive) passes.
#  3. That copy with an unapproved root file fails.
#  4. A temporary git checkout with an unapproved tracked root file fails.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { echo "test-check-root: FAIL - $*" >&2; exit 1; }

bash "$here/scripts/check-root.sh" "$here" >/dev/null || fail "clean tree did not pass"

# Archive-like copy: only allowed entries, no .git.
mkdir "$tmp/archive"
while read -r e; do
  [[ -z "$e" || "$e" == \#* ]] && continue
  [ -e "$here/$e" ] && cp -R "$here/$e" "$tmp/archive/"
done < "$here/scripts/root-allowlist.txt"
mkdir -p "$tmp/archive/dist"   # generated output is skipped
out="$(bash "$tmp/archive/scripts/check-root.sh" "$tmp/archive")" || fail "clean archive copy did not pass"
grep -q "filesystem" <<<"$out" || fail "archive copy did not use filesystem mode"

touch "$tmp/archive/notes-2026-01-01.txt"
if bash "$tmp/archive/scripts/check-root.sh" "$tmp/archive" >/dev/null 2>&1; then
  fail "unapproved root file in archive copy was accepted"
fi

# Git checkout with a tracked stray file.
mkdir "$tmp/repo"
cp -R "$tmp/archive/." "$tmp/repo/"
rm -f "$tmp/repo/notes-2026-01-01.txt"; rm -rf "$tmp/repo/dist"
git -C "$tmp/repo" init -q
git -C "$tmp/repo" add -A
git -C "$tmp/repo" -c user.name=t -c user.email=t@example.com commit -qm base
bash "$tmp/repo/scripts/check-root.sh" "$tmp/repo" >/dev/null || fail "clean git copy did not pass"
echo x > "$tmp/repo/build-log.txt"
git -C "$tmp/repo" add build-log.txt
if bash "$tmp/repo/scripts/check-root.sh" "$tmp/repo" >/dev/null 2>&1; then
  fail "unapproved tracked root file was accepted"
fi
echo "test-check-root: OK (clean tree passes, archive mode works, stray files fail)"
