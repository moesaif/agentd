#!/bin/bash
# ---
# name: failing-action-triage
# description: When a GitHub Action fails, fetch logs and identify root cause
# triggers:
#   - webhook: github.workflow_run
# env:
#   - GITHUB_TOKEN
# ---

set -e

CONCLUSION=$(echo "$AGENTD_EVENT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('workflow_run',{}).get('conclusion',''))" 2>/dev/null || echo "")

if [ "$CONCLUSION" != "failure" ]; then
    echo "Workflow did not fail (conclusion: $CONCLUSION), skipping"
    exit 0
fi

REPO=$(echo "$AGENTD_EVENT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('repository',{}).get('full_name',''))" 2>/dev/null || echo "")
RUN_ID=$(echo "$AGENTD_EVENT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('workflow_run',{}).get('id',''))" 2>/dev/null || echo "")
WORKFLOW=$(echo "$AGENTD_EVENT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('workflow_run',{}).get('name',''))" 2>/dev/null || echo "")
BRANCH=$(echo "$AGENTD_EVENT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('workflow_run',{}).get('head_branch',''))" 2>/dev/null || echo "")

echo "Workflow '$WORKFLOW' failed on branch '$BRANCH' in $REPO"
echo "Run ID: $RUN_ID"

if [ -z "$RUN_ID" ] || [ -z "$REPO" ]; then
    echo "Missing run ID or repo"
    exit 0
fi

# Fetch workflow logs
LOGS_URL=$(curl -sL -H "Authorization: token $GITHUB_TOKEN" \
    "https://api.github.com/repos/$REPO/actions/runs/$RUN_ID/logs" \
    -o /dev/null -w "%{redirect_url}" 2>/dev/null || echo "")

if [ -n "$LOGS_URL" ]; then
    echo "---LOGS---"
    curl -sL "$LOGS_URL" | python3 -c "
import sys, zipfile, io
data = sys.stdin.buffer.read()
try:
    z = zipfile.ZipFile(io.BytesIO(data))
    for name in z.namelist():
        content = z.read(name).decode('utf-8', errors='replace')
        # Only show lines with errors
        for line in content.split('\n'):
            if any(w in line.lower() for w in ['error', 'fail', 'fatal', 'exception']):
                print(f'[{name}] {line.strip()}')
except:
    print('Could not parse logs')
" 2>/dev/null | tail -50
fi

# Fetch failed jobs
echo "---JOBS---"
curl -sL -H "Authorization: token $GITHUB_TOKEN" \
    "https://api.github.com/repos/$REPO/actions/runs/$RUN_ID/jobs" | \
    python3 -c "
import sys, json
data = json.load(sys.stdin)
for job in data.get('jobs', []):
    if job['conclusion'] == 'failure':
        print(f\"Job: {job['name']} - FAILED\")
        for step in job.get('steps', []):
            if step.get('conclusion') == 'failure':
                print(f\"  Step: {step['name']} - FAILED\")
" 2>/dev/null
