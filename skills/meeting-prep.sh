#!/bin/bash
# ---
# name: meeting-prep
# description: Check for upcoming meetings and prepare context
# triggers:
#   - cron: "0 * * * *"
# ---

set -e

echo "Meeting Prep Check - $(date '+%H:%M')"
echo "======================================"

# Check macOS calendar for upcoming events (next 30 minutes)
if command -v osascript &>/dev/null; then
    EVENTS=$(osascript -e '
    set now to current date
    set later to now + (30 * 60)
    set output to ""
    tell application "Calendar"
        repeat with c in calendars
            set evts to (every event of c whose start date >= now and start date <= later)
            repeat with e in evts
                set output to output & (summary of e) & " at " & (start date of e as string) & linefeed
            end repeat
        end repeat
    end tell
    return output
    ' 2>/dev/null || echo "")

    if [ -n "$EVENTS" ]; then
        echo "Upcoming meetings:"
        echo "$EVENTS"
    else
        echo "No meetings in the next 30 minutes."
    fi
else
    echo "Calendar check not available on this platform"
fi

# Show recent activity for context
echo ""
echo "Recent activity:"
git log --oneline -5 2>/dev/null || echo "  (no git history)"
