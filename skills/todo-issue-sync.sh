#!/bin/bash
# ---
# name: todo-issue-sync
# description: Scan changed files for TODO/FIXME/HACK comments and report them
# triggers:
#   - filesystem: "*.go,*.ts,*.py,*.js,*.rs"
#   - git: commit
# ---

set -e

FILE=$(echo "$AGENTD_EVENT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('file',''))" 2>/dev/null || echo "")

if [ -n "$FILE" ] && [ -f "$FILE" ]; then
    echo "Scanning: $FILE"
    grep -n -E '(TODO|FIXME|HACK|XXX|BUG):' "$FILE" 2>/dev/null || true
else
    # On git commit, scan all recently changed files
    echo "Scanning recent changes for TODOs..."
    git diff --name-only HEAD~1 2>/dev/null | while read -r f; do
        if [ -f "$f" ]; then
            MATCHES=$(grep -n -E '(TODO|FIXME|HACK|XXX|BUG):' "$f" 2>/dev/null || true)
            if [ -n "$MATCHES" ]; then
                echo "---$f---"
                echo "$MATCHES"
            fi
        fi
    done
fi
