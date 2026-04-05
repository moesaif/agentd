#!/bin/bash
# ---
# name: daily-standup
# description: Generate daily standup notes from yesterday's git activity
# triggers:
#   - cron: "0 9 * * 1-5"
# ---

set -e

echo "Daily Standup - $(date '+%A, %B %d %Y')"
echo "========================================="
echo ""

# Yesterday's commits
echo "Commits since yesterday:"
git log --since="yesterday" --oneline --all 2>/dev/null || echo "  (no commits)"
echo ""

# Files changed
echo "Files changed:"
git diff --stat HEAD@{yesterday} HEAD 2>/dev/null || echo "  (no changes)"
echo ""

# Branches worked on
echo "Active branches:"
git for-each-ref --sort=-committerdate --format='%(refname:short) (%(committerdate:relative))' refs/heads/ 2>/dev/null | head -5 || echo "  (none)"
echo ""

# In-progress work (unstaged/staged changes)
CHANGES=$(git status --short 2>/dev/null || echo "")
if [ -n "$CHANGES" ]; then
    echo "Work in progress:"
    echo "$CHANGES"
fi
