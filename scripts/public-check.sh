#!/usr/bin/env bash
# Checks that the repository is safe to make (and keep) public: nothing
# private is tracked or in the history, and .gitignore covers what must stay
# local. Run it before pushing (make public-check; make hooks runs it on
# every git push). It only reads; it exits 1 if it finds anything.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)"

problems=0
fail() { echo "✗ $*"; problems=$((problems + 1)); }
ok() { echo "✓ $*"; }

# 1. Files that must never be tracked.
sensitive='(^|/)(\.env(\..*)?|.*\.tfvars|.*\.tfstate(\..*)?|.*\.tfplan|.*\.pem|.*\.key|id_(rsa|ed25519)|credentials|.*\.p12)$'
tracked=$(git ls-files | grep -E "$sensitive" | grep -vE '\.tfvars\.example$' || true)
if [ -n "$tracked" ]; then
  fail "private files are tracked:"; echo "$tracked" | sed 's/^/    /'
else
  ok "no private files are tracked"
fi
built=$(git ls-files | grep -E '^(bin|dist)/|(^|/)\.terraform/' || true)
[ -n "$built" ] && fail "build output is tracked: $(echo "$built" | head -3 | tr '\n' ' ')" || ok "no build output is tracked"

# 2. .gitignore covers them.
for pattern in '.env' '*.tfvars' '*.tfstate' '*.tfplan' '.terraform/' 'bin/' 'dist/'; do
  probe="${pattern//\*/probe}"
  probe="${probe%/}"
  if ! git check-ignore -q --no-index "$probe" && ! git check-ignore -q --no-index "infra/prod/$probe"; then
    fail ".gitignore doesn't cover $pattern"
  fi
done
ok ".gitignore checked"

# 3. Secrets and personal details in tracked files and in every commit.
# Your own addresses come from the git-ignored tfvars, so they're checked
# without being written here.
patterns=(
  'AKIA[0-9A-Z]{16}' 'ASIA[0-9A-Z]{16}'                       # AWS access key IDs
  'aws_secret_access_key[[:space:]]*=[[:space:]]*[A-Za-z0-9/+]{40}'
  '-----BEGIN ([A-Z]+ )?PRIVATE KEY-----'
  'gh[pousr]_[A-Za-z0-9]{36}' 'github_pat_[A-Za-z0-9_]{60,}'   # GitHub tokens
  'xox[baprs]-[A-Za-z0-9-]{10,}'                              # Slack tokens
  'sk_live_[A-Za-z0-9]{20,}'                                  # Stripe keys
)
for f in infra/*/terraform.tfvars; do
  [ -f "$f" ] || continue
  while IFS= read -r email; do
    patterns+=("$(printf '%s' "$email" | sed 's/[.[\*^$()+?{|]/\\&/g')")
  done < <(grep -Eo '[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}' "$f" | sort -u)
done
regex=$(IFS='|'; echo "${patterns[*]}")
now=$(git grep -n -I -E "$regex" -- . ':!scripts/public-check.sh' || true)
if [ -n "$now" ]; then
  fail "secrets or personal details in tracked files:"; echo "$now" | cut -c1-160 | sed 's/^/    /'
else
  ok "no secrets in tracked files"
fi
history=$(git log --all -p --no-color -G "$regex" --format='%h %s' -- . ':!scripts/public-check.sh' | grep -E "^[0-9a-f]{7,} |^\+.*($regex)" | cut -c1-160 || true)
if [ -n "$history" ]; then
  fail "secrets or personal details in the history (rewrite it before going public):"; echo "$history" | head -20 | sed 's/^/    /'
else
  ok "no secrets anywhere in the history"
fi

if [ "$problems" -gt 0 ]; then
  echo; echo "$problems problem(s). Fix them before pushing or making the repo public."
  exit 1
fi
echo; echo "Safe to publish."
