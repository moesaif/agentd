package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/moesaif/agentd/internal/db"
	"github.com/moesaif/agentd/internal/skills"
	"github.com/moesaif/agentd/internal/watchers"
)

const systemPromptTemplate = `You are agentd, a proactive developer agent running on %s's machine.
You observe events from their development environment and take helpful actions using the tools provided.

Current context:
- Working directory: %s
- Recent events: %s
- Loaded skills: %s
- Persistent memory: %s

Guidelines:
- Be concise. Prefer doing over asking.
- When in doubt, use the log or notify tool rather than take irreversible actions.
- When skill output includes a TASK instruction, follow it.
- When posting to Slack webhooks, use {"text": "..."} with Slack mrkdwn formatting.
- When posting to Discord webhooks, use {"content": "..."} with Discord markdown.
- Always call at least one tool — either act on the event or log why no action was needed.`

func BuildSystemPrompt(agentName string, loadedSkills []skills.Skill, store *db.DB) string {
	cwd, _ := os.Getwd()

	recentEvents := "none"
	if events, err := store.RecentEvents(5); err == nil && len(events) > 0 {
		var parts []string
		for _, e := range events {
			parts = append(parts, fmt.Sprintf("[%s] %s.%s", e.CreatedAt.Format("15:04"), e.Source, e.Type))
		}
		recentEvents = strings.Join(parts, ", ")
	}

	var skillNames []string
	for _, s := range loadedSkills {
		skillNames = append(skillNames, s.Manifest.Name)
	}
	skillList := "none"
	if len(skillNames) > 0 {
		skillList = strings.Join(skillNames, ", ")
	}

	memorySnapshot := "empty"
	if mem, err := store.AllMemory(); err == nil && len(mem) > 0 {
		data, _ := json.Marshal(mem)
		memorySnapshot = string(data)
	}

	return fmt.Sprintf(systemPromptTemplate, agentName, cwd, recentEvents, skillList, memorySnapshot)
}

func BuildEventMessage(event watchers.Event, skillOutput string) string {
	eventJSON, _ := json.MarshalIndent(event, "", "  ")
	msg := fmt.Sprintf("Event received:\n```json\n%s\n```", string(eventJSON))
	if skillOutput != "" {
		msg += fmt.Sprintf("\n\nSkill output:\n```\n%s\n```", skillOutput)
	}
	return msg
}
