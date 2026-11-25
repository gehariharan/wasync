#!/usr/bin/env bash
# Exercises scripts/publish-tap.sh against a local bare tap that also holds a
# second cask. Needs only bash, git and python3; never contacts a real remote.
set -euo pipefail
project_dir="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
export GIT_CONFIG_GLOBAL="$work/gitconfig" GIT_CONFIG_NOSYSTEM=1
git config --global user.name 'tap test'
git config --global user.email 'tap-test@example.invalid'
git config --global init.defaultBranch main
git config --global advice.detachedHead false
real_git="$(command -v git)"

fail() { echo "FAIL: $*" >&2; exit 1; }
pass() { echo "ok - $*"; }

tap="$work/tap.git"
export WASYNC_TAP_REMOTE="$tap"
unset HOMEBREW_TAP_SSH_KEY || true

# Seed the bare tap like the real one, plus a second cask and a formula.
seed="$work/seed"
git init -q "$seed"
mkdir -p "$seed/Casks" "$seed/Formula" "$seed/lib"
printf '# Source helper newer than the release checkout\n' > "$seed/lib/wasync_github_release_download_strategy.rb"
printf 'cask "wasync" do\n  version "0.1.0-preview.20261018060349"\nend\n' > "$seed/Casks/wasync.rb"
printf 'cask "whatsapp-axi" do\n  version "0.1.0"\nend\n' > "$seed/Casks/whatsapp-axi.rb"
printf 'class Example < Formula\nend\n' > "$seed/Formula/example.rb"
printf '# Custom tap README for wasync and whatsapp-axi\n' > "$seed/README.md"
git -C "$seed" add -A
git -C "$seed" commit -q -m seed
git clone -q --bare "$seed" "$tap"

make_assets() { # version -> prints asset directory
  local dir="$work/assets-$1"
  mkdir -p "$dir"
  printf 'arm64 %s' "$1" > "$dir/wasync_$1_darwin_arm64.tar.gz"
  printf 'amd64 %s' "$1" > "$dir/wasync_$1_darwin_amd64.tar.gz"
  python3 "$project_dir/scripts/homebrew-cask.py" "$1" "$dir" "$dir/wasync.rb"
  python3 "$project_dir/scripts/homebrew-formulas.py" "$1" "$(printf '1%.0s' {1..64})" "$dir"
  echo "$dir"
}
tap_file() { "$real_git" --git-dir="$tap" show "main:$1"; }
tap_head() { "$real_git" --git-dir="$tap" rev-parse main; }

# A concurrent publisher (the whatsapp-axi release) pushes between our clone
# and our first push. A PATH shim around git does this exactly once, so the
# first push is deterministically rejected as non-fast-forward.
shim="$work/shim"
mkdir -p "$shim"
cat > "$shim/git" <<SHIM
#!/usr/bin/env bash
if [[ "\${1:-}" == push && ! -e "$work/raced" ]]; then
  touch "$work/raced"
  other="$work/other-publisher"
  "$real_git" clone -q "$tap" "\$other"
  printf 'cask "whatsapp-axi" do\n  version "0.2.0"\nend\n' > "\$other/Casks/whatsapp-axi.rb"
  printf '# Canonical helper changed on main\n' > "\$other/lib/wasync_github_release_download_strategy.rb"
  "$real_git" -C "\$other" commit -q -am 'Update whatsapp-axi 0.2.0'
  "$real_git" -C "\$other" push -q origin HEAD:main
fi
exec "$real_git" "\$@"
SHIM
chmod +x "$shim/git"

# 1. Normal publish racing a second publisher.
v1=0.1.0-preview.20261019000000
assets1="$(make_assets "$v1")"
readme_before="$(tap_file README.md)"
out="$(PATH="$shim:$PATH" bash "$project_dir/scripts/publish-tap.sh" "$assets1" 2>&1)" || { echo "$out"; fail "publish $v1"; }
[[ -e "$work/raced" ]] || fail "race shim did not run"
[[ "$out" == *"attempt 2"* ]] || { echo "$out"; fail "expected retry after non-fast-forward"; }
pass "non-fast-forward push retried after rebase"
diff <(tap_file Casks/wasync.rb) "$assets1/wasync.rb" >/dev/null || fail "wasync.rb not updated"
pass "Casks/wasync.rb updated to $v1"
diff <(tap_file Formula/wasync.rb) "$assets1/wasync-formula.rb" >/dev/null || fail "core formula not updated"
diff <(tap_file Formula/wasync-axi.rb) "$assets1/wasync-axi.rb" >/dev/null || fail "AXI formula not updated"
diff <(tap_file Formula/whatsapp-axi.rb) "$assets1/whatsapp-axi.rb" >/dev/null || fail "old-name AXI formula not updated"
pass "all source formulas published with the cask"
[[ "$(tap_file lib/wasync_github_release_download_strategy.rb)" == "# Canonical helper changed on main" ]] || fail "publisher overwrote a concurrent source change"
pass "canonical download helper source preserved across a racing release"
[[ "$(tap_file Casks/whatsapp-axi.rb)" == *'version "0.2.0"'* ]] || fail "concurrent whatsapp-axi update lost"
pass "Casks/whatsapp-axi.rb kept the other publisher's version"
[[ "$(tap_file Formula/example.rb)" == *"class Example"* ]] || fail "Formula changed"
pass "Formula/ untouched"
[[ "$(tap_file README.md)" == "$readme_before" ]] || fail "README overwritten"
pass "existing README.md untouched"
diff <(tap_file LICENSE) "$project_dir/LICENSE" >/dev/null || fail "missing LICENSE not seeded"
pass "missing LICENSE added"

# 2. Existing LICENSE is not overwritten; second cask untouched by a plain publish.
edit="$work/edit"
git clone -q "$tap" "$edit"
printf 'Custom tap licence\n' > "$edit/LICENSE"
git -C "$edit" commit -q -am 'Custom licence'
git -C "$edit" push -q origin HEAD:main
axi_before="$(tap_file Casks/whatsapp-axi.rb)"
v2=0.1.0-preview.20261020000000
assets2="$(make_assets "$v2")"
bash "$project_dir/scripts/publish-tap.sh" "$assets2" >/dev/null 2>&1 || fail "publish $v2"
[[ "$(tap_file LICENSE)" == "Custom tap licence" ]] || fail "existing LICENSE overwritten"
pass "existing LICENSE untouched"
[[ "$(tap_file Casks/whatsapp-axi.rb)" == "$axi_before" ]] || fail "whatsapp-axi.rb modified"
pass "Casks/whatsapp-axi.rb untouched"
diff <(tap_file Casks/wasync.rb) "$assets2/wasync.rb" >/dev/null || fail "wasync.rb not updated to $v2"

# 3. Stale (older) version is refused without pushing.
head_before="$(tap_head)"
out="$(bash "$project_dir/scripts/publish-tap.sh" "$assets1" 2>&1)" || { echo "$out"; fail "stale publish should exit 0"; }
[[ "$out" == *"newer than $v1"* ]] || { echo "$out"; fail "expected stale message"; }
[[ "$(tap_head)" == "$head_before" ]] || fail "stale version was pushed"
pass "older version not pushed (exit 0)"

# 4. Republishing the same version is a no-op.
bash "$project_dir/scripts/publish-tap.sh" "$assets2" >/dev/null 2>&1 || fail "republish $v2"
[[ "$(tap_head)" == "$head_before" ]] || fail "republish created a commit"
pass "same version is a no-op"

# 5. Persistent rejection fails clearly after three attempts.
cat > "$tap/hooks/pre-receive" <<'HOOK'
#!/usr/bin/env bash
echo "rejected by test hook" >&2
exit 1
HOOK
chmod +x "$tap/hooks/pre-receive"
v3=0.1.0-preview.20261021000000
assets3="$(make_assets "$v3")"
if out="$(bash "$project_dir/scripts/publish-tap.sh" "$assets3" 2>&1)"; then
  fail "publish should fail when every push is rejected"
fi
[[ "$out" == *"after 3 attempts"* ]] || { echo "$out"; fail "expected clear failure message"; }
[[ "$(tap_head)" == "$head_before" ]] || fail "rejected push changed the tap"
pass "gives up clearly after 3 rejected pushes"
rm "$tap/hooks/pre-receive"

# 6. A concurrent publish of a newer wasync lands between clone and push: the
#    rebase conflicts, the publisher rebuilds on the new tip and then refuses
#    to downgrade it.
v4=0.1.0-preview.20261022000000
v5=0.1.0-preview.20261023000000
assets4="$(make_assets "$v4")"
assets5="$(make_assets "$v5")"
shim2="$work/shim2"
mkdir -p "$shim2"
cat > "$shim2/git" <<SHIM
#!/usr/bin/env bash
if [[ "\${1:-}" == push && ! -e "$work/raced2" ]]; then
  touch "$work/raced2"
  other="$work/newer-wasync"
  "$real_git" clone -q "$tap" "\$other"
  cp "$assets5/wasync.rb" "\$other/Casks/wasync.rb"
  "$real_git" -C "\$other" commit -q -am 'Update prebuilt wasync release $v5'
  "$real_git" -C "\$other" push -q origin HEAD:main
fi
exec "$real_git" "\$@"
SHIM
chmod +x "$shim2/git"
out="$(PATH="$shim2:$PATH" bash "$project_dir/scripts/publish-tap.sh" "$assets4" 2>&1)" || { echo "$out"; fail "racing older publish should exit 0"; }
[[ -e "$work/raced2" ]] || fail "second race shim did not run"
[[ "$out" == *"newer than $v4"* ]] || { echo "$out"; fail "expected stale message after race"; }
diff <(tap_file Casks/wasync.rb) "$assets5/wasync.rb" >/dev/null || fail "newer concurrent wasync was overwritten"
pass "newer concurrent wasync publish is not downgraded"

echo "All publish-tap tests passed."
