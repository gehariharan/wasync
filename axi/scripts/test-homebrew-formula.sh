#!/usr/bin/env bash
set -euo pipefail
project_dir="$(cd "$(dirname "$0")/.." && pwd)"
gen="$project_dir/scripts/homebrew-formula.py"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok - $*"; }

version=1.2.3
sum=1111111111111111111111111111111111111111111111111111111111111111
commit=0123456789ab
python3 "$gen" "$version" "$sum" "$work/wasync-axi.rb" --commit "$commit"
python3 "$gen" "$version" "$sum" "$work/no-commit.rb"
python3 "$project_dir/../scripts/homebrew-formulas.py" "$version" "$sum" "$work/combined" --commit "$commit"
ruby "$project_dir/scripts/test-homebrew-formula.rb" "$work" "$version" "$sum" "$commit"
pass "evaluated formula metadata, platform dependencies and build commands"

python3 "$gen" "$version" "$sum" "$work/again.rb" --commit "$commit"
cmp -s "$work/wasync-axi.rb" "$work/again.rb" || fail "rendering is not deterministic"
pass "rendering is deterministic"

# Bad input is refused and writes nothing.
expect_refusal() { # description, args...
  local what="$1"; shift
  rm -f "$work/bad.rb"
  if python3 "$gen" "$@" 2>/dev/null; then fail "accepted $what"; fi
  [[ ! -e "$work/bad.rb" ]] || fail "wrote a formula for $what"
  pass "refuses $what"
}
expect_refusal "a leading v" "v$version" "$sum" "$work/bad.rb"
expect_refusal "a short checksum" "$version" 1234 "$work/bad.rb"
expect_refusal "an uppercase checksum" "$version" "${sum//1/A}" "$work/bad.rb"
expect_refusal "a bad commit" "$version" "$sum" "$work/bad.rb" --commit 'main; rm'
echo "homebrew formula tests passed"
