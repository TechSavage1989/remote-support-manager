#!/usr/bin/env bash
# Rejects unexpected files or folders at the repository root.
#
#   scripts/check-root.sh [repo-dir]
#
# With Git metadata it checks tracked files (git ls-files). In an extracted
# source archive without .git it checks the filesystem instead, skipping only
# the generated/local paths listed in GENERATED below. It never skips the
# check silently: the mode used is printed.
set -euo pipefail
root="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
allow="$root/scripts/root-allowlist.txt"
[ -f "$allow" ] || { echo "check-root: missing $allow" >&2; exit 2; }
# Generated or per-machine paths that may exist in a working folder but are
# never tracked (they are also in .gitignore).
GENERATED=".git dist build rsm-data .vscode .idea"

mapfile -t allowed < <(grep -v '^\s*#' "$allow" | sed 's/\s*$//' | grep -v '^$')

if git -C "$root" rev-parse --is-inside-work-tree >/dev/null 2>&1 && [ -e "$root/.git" ]; then
  mode="git (tracked files)"
  mapfile -t entries < <(git -C "$root" ls-files | cut -d/ -f1 | sort -u)
else
  mode="filesystem (no .git; generated paths skipped: $GENERATED)"
  mapfile -t entries < <(cd "$root" && ls -A1 | sort -u)
fi

bad=()
for e in "${entries[@]}"; do
  [ -z "$e" ] && continue
  skip=0
  if [[ "$mode" == filesystem* ]]; then
    for g in $GENERATED; do [ "$e" = "$g" ] && skip=1; done
  fi
  [ $skip -eq 1 ] && continue
  ok=0
  for a in "${allowed[@]}"; do [ "$e" = "$a" ] && ok=1 && break; done
  [ $ok -eq 0 ] && bad+=("$e")
done

echo "check-root: mode = $mode"
if [ ${#bad[@]} -gt 0 ]; then
  echo "check-root: FAIL - unexpected root entries:" >&2
  printf '  %s\n' "${bad[@]}" >&2
  echo "Move them into cmd/, internal/, docs/, scripts/ or tests/, ignore them in .gitignore, or add them to scripts/root-allowlist.txt with a reason in AGENTS.md." >&2
  exit 1
fi
echo "check-root: OK (${#entries[@]} root entries checked)"
