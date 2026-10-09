#!/bin/sh
# Install DayMug's tracked git hooks into this clone's .git/hooks.
#
# We copy (rather than set core.hooksPath) so any other local hooks in
# .git/hooks keep working; an existing copy of a managed hook is backed up to
# <hook>.bak before being overwritten.
#
# Hooks installed:
#   pre-commit  — i18n parity + hardcoded-string guard + Go/TS API contract
#
# Idempotent: re-run after pulling hook updates.

set -e

repo_root=$(git rev-parse --show-toplevel)
src="$repo_root/.githooks"
dest="$repo_root/.git/hooks"

for hook in pre-commit; do
  if [ -e "$dest/$hook" ] && ! cmp -s "$src/$hook" "$dest/$hook"; then
    echo "Backing up existing $hook -> $hook.bak"
    cp "$dest/$hook" "$dest/$hook.bak"
  fi
  cp "$src/$hook" "$dest/$hook"
  chmod +x "$dest/$hook"
  echo "Installed $hook"
done

echo "Done. Bypass i18n with DAYMUG_SKIP_I18N=1 or 'git commit --no-verify'."
