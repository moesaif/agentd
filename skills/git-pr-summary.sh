#!/bin/bash
# ---
# name: git-pr-summary
# description: When a PR is opened, summarize the diff and post a comment
# triggers:
#   - webhook: github.pull_request
# env:
#   - GITHUB_TOKEN
# ---

set -e

# Parse the event
PR_URL=$(echo "$AGENTD_EVENT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('pull_request',{}).get('html_url',''))" 2>/dev/null || echo "")
PR_NUM=$(echo "$AGENTD_EVENT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('number',''))" 2>/dev/null || echo "")
REPO=$(echo "$AGENTD_EVENT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('repository',{}).get('full_name',''))" 2>/dev/null || echo "")

if [ -z "$PR_NUM" ] || [ -z "$REPO" ]; then
    echo "No PR number or repo found in event"
    exit 0
fi

echo "PR #$PR_NUM opened in $REPO"
echo "URL: $PR_URL"

# Fetch the diff
DIFF=$(curl -sL -H "Authorization: token $GITHUB_TOKEN" \
    -H "Accept: application/vnd.github.v3.diff" \
    "https://api.github.com/repos/$REPO/pulls/$PR_NUM")

echo "---DIFF---"
echo "$DIFF" | head -200

# Fetch the files changed
FILES=$(curl -sL -H "Authorization: token $GITHUB_TOKEN" \
    "https://api.github.com/repos/$REPO/pulls/$PR_NUM/files" | \
    python3 -c "import sys,json; [print(f['filename']) for f in json.load(sys.stdin)]" 2>/dev/null || echo "")

echo "---FILES---"
echo "$FILES"
