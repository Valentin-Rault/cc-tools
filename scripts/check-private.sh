#!/usr/bin/env bash
# Fail when the index (or, with --history, any commit) contains a word listed in .private-words, or a binary file:
# build output embeds local paths, and grep can't vouch for it. .private-words is gitignored on purpose: the words
# themselves are what must not be published.
#   check-private.sh            staged/tracked content, file names and the commit identity (pre-commit hook)
#   check-private.sh --history  every commit's content, file names, authors and messages (before publishing)
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 2
[ -s .private-words ] || { echo "check-private: .private-words is missing or empty" >&2; exit 2; }
words=$(mktemp); trap 'rm -f "$words"' EXIT
grep -v '^[[:space:]]*#' .private-words | grep . > "$words"

found=0
hit() { echo "check-private: $1" >&2; found=1; }

git grep --cached -I -n -i -F -f "$words" && hit "tracked content above"
git ls-files | grep -i -F -f "$words" && hit "file names above"
git ls-files --eol | awk -F'\t' '$1 ~ /^i\/-text/ {print $2}' | grep . && hit "binary files above"
printf '%s\n' "$(git config user.name)" "$(git config user.email)" | grep -i -F -f "$words" && hit "commit identity above"

if [ "${1:-}" = --history ] && git rev-parse -q --verify HEAD >/dev/null; then
  git grep -I -n -i -F -f "$words" $(git rev-list --all) && hit "content in history above"
  git log --all --format='%an <%ae> %cn <%ce>%n%B' | grep -i -F -f "$words" && hit "authors or messages in history above"
  git log --all --name-only --format= | grep -i -F -f "$words" && hit "file names in history above"
  git log --all --numstat --format= | awk -F'\t' '$1 == "-" {print $3}' | sort -u | grep . && hit "binary files in history above"
fi
exit $found
