#!/bin/bash
# ---
# name: git-push-summary
# description: Summarize every commit and post to Slack (or notify locally)
# triggers:
#   - git: commit
# timeout: 30
# ---
#
# Set SLACK_WEBHOOK_URL in your environment to post to Slack.
# Without it, agentd will send a desktop notification instead.

set -e

# ── parse event payload (manual trigger has no sha/prev) ──────────────────────
SHA=""
PREV=""
BRANCH=""
MSG=""

if [ -n "$AGENTD_EVENT" ]; then
    SHA=$(echo "$AGENTD_EVENT"   | python3 -c "import sys,json; print(json.load(sys.stdin).get('sha',''))"     2>/dev/null || true)
    PREV=$(echo "$AGENTD_EVENT"  | python3 -c "import sys,json; print(json.load(sys.stdin).get('prev',''))"    2>/dev/null || true)
    BRANCH=$(echo "$AGENTD_EVENT"| python3 -c "import sys,json; print(json.load(sys.stdin).get('branch',''))"  2>/dev/null || true)
    MSG=$(echo "$AGENTD_EVENT"   | python3 -c "import sys,json; print(json.load(sys.stdin).get('message',''))" 2>/dev/null || true)
fi

# Fall back to HEAD for manual runs or missing data
if [ -z "$SHA" ]; then
    SHA=$(git rev-parse HEAD 2>/dev/null || echo "")
fi
if [ -z "$BRANCH" ]; then
    BRANCH=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")
fi
if [ -z "$MSG" ]; then
    MSG=$(git log -1 --format='%s' 2>/dev/null || echo "")
fi
if [ -z "$PREV" ]; then
    PREV=$(git rev-parse HEAD~1 2>/dev/null || echo "")
fi

# ── task instruction for the LLM ─────────────────────────────────────────────
echo "TASK: Write a concise summary of this commit. Then:"
if [ -n "$SLACK_WEBHOOK_URL" ]; then
    echo "  - POST a formatted Slack message to the webhook below."
    echo "  - Use Slack mrkdwn: *bold* for headings, \`code\` for refs, > for the summary."
    echo ""
    echo "SLACK_WEBHOOK: $SLACK_WEBHOOK_URL"
else
    echo "  - Send a desktop notification with the summary (no Slack configured)."
fi

REPO_NAME=$(basename "$(git rev-parse --show-toplevel 2>/dev/null)" 2>/dev/null || echo "repo")
AUTHOR=$(git log -1 --format='%an' "$SHA" 2>/dev/null || echo "unknown")

# ── commit metadata ──────────────────────────────────────────────────────────
echo ""
echo "=== COMMIT ==="
echo "Repo:    $REPO_NAME"
echo "Branch:  $BRANCH"
echo "SHA:     $(printf '%.8s' "$SHA")"
echo "Author:  $AUTHOR"
echo "Message: $MSG"

# ── files changed ────────────────────────────────────────────────────────────
echo ""
echo "=== FILES CHANGED ==="
if [ -n "$PREV" ]; then
    git diff --stat "$PREV" "$SHA" 2>/dev/null || git diff --stat HEAD~1 HEAD 2>/dev/null || echo "(no diff available)"
else
    git diff --stat HEAD~1 HEAD 2>/dev/null || echo "(first commit)"
fi

# ── diff (truncated for LLM context window) ──────────────────────────────────
echo ""
echo "=== DIFF ==="
if [ -n "$PREV" ]; then
    git diff "$PREV" "$SHA" 2>/dev/null | head -150
else
    git diff HEAD~1 HEAD 2>/dev/null | head -150
fi
