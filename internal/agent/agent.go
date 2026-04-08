package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/moesaif/agentd/internal/config"
	"github.com/moesaif/agentd/internal/db"
	"github.com/moesaif/agentd/internal/llm"
	"github.com/moesaif/agentd/internal/skills"
	"github.com/moesaif/agentd/internal/watchers"
)

type Agent struct {
	config   config.Config
	db       *db.DB
	llm      llm.Client
	skills   []skills.Skill
	watchers []watchers.Watcher
	events   chan watchers.Event
	done     chan struct{}
}

func New(cfg config.Config, store *db.DB, llmClient llm.Client, loadedSkills []skills.Skill) *Agent {
	return &Agent{
		config: cfg,
		db:     store,
		llm:    llmClient,
		skills: loadedSkills,
		events: make(chan watchers.Event, 100),
		done:   make(chan struct{}),
	}
}

func (a *Agent) AddWatcher(w watchers.Watcher) {
	a.watchers = append(a.watchers, w)
}

func (a *Agent) Events() chan watchers.Event {
	return a.events
}

func (a *Agent) Start(ctx context.Context) error {
	// Start all watchers
	for _, w := range a.watchers {
		if err := w.Start(a.events); err != nil {
			log.Error("failed to start watcher", "name", w.Name(), "error", err)
			continue
		}
	}

	// Fire startup events for skills with @startup trigger
	a.fireStartupEvents()

	// Main event loop
	go a.loop(ctx)

	log.Info("agent started",
		"name", a.config.Agent.Name,
		"skills", len(a.skills),
		"watchers", len(a.watchers),
	)

	return nil
}

func (a *Agent) Stop() error {
	close(a.done)
	for _, w := range a.watchers {
		if err := w.Stop(); err != nil {
			log.Error("failed to stop watcher", "name", w.Name(), "error", err)
		}
	}
	return nil
}

func (a *Agent) loop(ctx context.Context) {
	for {
		select {
		case <-a.done:
			return
		case <-ctx.Done():
			return
		case event := <-a.events:
			a.handleEvent(ctx, event)
		}
	}
}

func (a *Agent) handleEvent(ctx context.Context, event watchers.Event) {
	log.Info("event received", "source", event.Source, "type", event.Type)

	// Store event
	eventID, err := a.db.InsertEvent(event.Source, event.Type, event.Payload)
	if err != nil {
		log.Error("failed to store event", "error", err)
	}

	// Find matching skills
	matched := skills.FindMatching(a.skills, event.Source, event.Type, event.Payload)
	if len(matched) == 0 {
		log.Debug("no matching skills for event", "source", event.Source, "type", event.Type)
		return
	}

	log.Info("matched skills", "count", len(matched), "skills", skillNames(matched))

	for _, skill := range matched {
		a.executeSkill(ctx, skill, event, eventID)
	}
}

func (a *Agent) executeSkill(ctx context.Context, skill skills.Skill, event watchers.Event, eventID int64) {
	log.Info("executing skill", "name", skill.Manifest.Name)

	envVars := map[string]string{
		"AGENTD_CONFIG_DIR": a.config.StateDir(),
		"AGENTD_STATE_DIR":  a.config.StateDir(),
	}

	result, err := skills.Run(ctx, skill, event.Payload, envVars)
	if err != nil {
		log.Error("skill execution failed", "name", skill.Manifest.Name, "error", err)
		a.db.InsertAction(eventID, skill.Manifest.Name, "", "error", map[string]any{"error": err.Error()}, "failed")
		return
	}

	if result.ExitCode != 0 {
		log.Warn("skill exited with non-zero code",
			"name", skill.Manifest.Name,
			"exit_code", result.ExitCode,
			"stderr", result.Stderr,
		)
	}

	// Send skill output to LLM for processing
	if a.llm != nil {
		a.processWithLLM(ctx, skill, event, result, eventID)
	} else {
		// No LLM configured, just log the output
		log.Info("skill output", "name", skill.Manifest.Name, "stdout", result.Stdout)
		a.db.InsertAction(eventID, skill.Manifest.Name, result.Stdout, "log", map[string]any{"output": result.Stdout}, "completed")
	}
}

// agentTools defines the actions the LLM can take via native tool-use.
var agentTools = []llm.ToolDefinition{
	{
		Name:        "shell",
		Description: "Run a shell command on the host machine",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to execute",
				},
			},
			"required": []string{"command"},
		},
	},
	{
		Name:        "http",
		Description: "Make an outbound HTTP request (Slack webhook, GitHub API, etc.)",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url":     map[string]any{"type": "string", "description": "Request URL"},
				"method":  map[string]any{"type": "string", "enum": []string{"GET", "POST", "PUT", "PATCH", "DELETE"}, "description": "HTTP method (default GET)"},
				"headers": map[string]any{"type": "object", "description": "HTTP headers as key/value strings"},
				"body":    map[string]any{"description": "Request body — string or JSON object"},
			},
			"required": []string{"url"},
		},
	},
	{
		Name:        "notify",
		Description: "Send a desktop notification to the user",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string", "description": "Notification body text"},
				"title":   map[string]any{"type": "string", "description": "Notification title (default: agentd)"},
			},
			"required": []string{"message"},
		},
	},
	{
		Name:        "log",
		Description: "Log a message when no further action is needed",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string", "description": "Message to log"},
			},
			"required": []string{"message"},
		},
	},
}

func (a *Agent) processWithLLM(ctx context.Context, skill skills.Skill, event watchers.Event, result skills.RunResult, eventID int64) {
	systemPrompt := BuildSystemPrompt(a.config.Agent.Name, a.skills, a.db)
	userMessage := BuildEventMessage(event, result.Stdout)

	resp, err := a.llm.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: systemPrompt,
		Messages:     []llm.Message{{Role: "user", Content: userMessage}},
		MaxTokens:    2048,
		Temperature:  0.3,
		Tools:        agentTools,
	})
	if err != nil {
		log.Error("LLM processing failed", "error", err)
		a.db.InsertAction(eventID, skill.Manifest.Name, "", "error", map[string]any{"error": err.Error()}, "failed")
		return
	}

	log.Debug("LLM response", "tool_calls", len(resp.ToolCalls), "tokens", resp.TokensUsed)

	// Prefer native tool calls; fall back to ACTION: line parsing (e.g. Ollama)
	if len(resp.ToolCalls) > 0 {
		for _, tc := range resp.ToolCalls {
			a.executeAction(ctx, ParsedAction{Type: tc.Name, Payload: tc.Input}, skill.Manifest.Name, eventID, resp.Content)
		}
		return
	}

	actions := parseActions(resp.Content)
	if len(actions) == 0 {
		a.db.InsertAction(eventID, skill.Manifest.Name, resp.Content, "log", map[string]any{"response": resp.Content}, "completed")
		return
	}
	for _, action := range actions {
		a.executeAction(ctx, action, skill.Manifest.Name, eventID, resp.Content)
	}
}

type ParsedAction struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

func parseActions(content string) []ParsedAction {
	var actions []ParsedAction

	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ACTION:") {
			continue
		}

		jsonStr := strings.TrimPrefix(line, "ACTION:")
		jsonStr = strings.TrimSpace(jsonStr)

		var action ParsedAction
		if err := json.Unmarshal([]byte(jsonStr), &action); err != nil {
			log.Warn("failed to parse action", "line", line, "error", err)
			continue
		}
		actions = append(actions, action)
	}

	return actions
}

func (a *Agent) executeAction(ctx context.Context, action ParsedAction, skillName string, eventID int64, llmResponse string) {
	log.Info("executing action", "type", action.Type, "skill", skillName)

	actionID, _ := a.db.InsertAction(eventID, skillName, llmResponse, action.Type, action.Payload, "pending")

	var err error
	switch action.Type {
	case "shell":
		err = a.executeShellAction(ctx, action.Payload)
	case "http":
		err = a.executeHTTPAction(ctx, action.Payload)
	case "notify":
		a.executeNotifyAction(action.Payload)
	case "log":
		msg, _ := action.Payload["message"].(string)
		log.Info("agent log", "message", msg)
	default:
		log.Warn("unknown action type", "type", action.Type)
	}

	status := "completed"
	if err != nil {
		status = "failed"
		log.Error("action failed", "type", action.Type, "error", err)
	}

	a.db.UpdateActionStatus(actionID, status)
}

func (a *Agent) executeShellAction(ctx context.Context, payload map[string]any) error {
	command, ok := payload["command"].(string)
	if !ok {
		return fmt.Errorf("shell action missing 'command' field")
	}

	log.Info("running shell command", "command", command)
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir, _ = os.Getwd()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command failed: %w\noutput: %s", err, string(out))
	}
	log.Debug("shell output", "output", string(out))
	return nil
}

func (a *Agent) executeHTTPAction(ctx context.Context, payload map[string]any) error {
	url, ok := payload["url"].(string)
	if !ok || url == "" {
		return fmt.Errorf("http action missing 'url' field")
	}

	method := "GET"
	if m, ok := payload["method"].(string); ok && m != "" {
		method = strings.ToUpper(m)
	}

	// Build body
	var bodyReader io.Reader
	switch v := payload["body"].(type) {
	case string:
		if v != "" {
			bodyReader = strings.NewReader(v)
		}
	case map[string]any:
		data, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("marshaling http body: %w", err)
		}
		bodyReader = bytes.NewReader(data)
	}

	// Timeout override
	if secs, ok := payload["timeout"].(float64); ok && secs > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(secs)*time.Second)
		defer cancel()
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("building http request: %w", err)
	}

	// Default Content-Type when body is an object
	if _, isObj := payload["body"].(map[string]any); isObj {
		req.Header.Set("Content-Type", "application/json")
	}

	// Custom headers
	if headers, ok := payload["headers"].(map[string]any); ok {
		for k, v := range headers {
			if s, ok := v.(string); ok {
				req.Header.Set(k, s)
			}
		}
	}

	log.Info("http action", "method", method, "url", url)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	log.Debug("http response", "status", resp.StatusCode, "body", strings.TrimSpace(string(respBody)))

	if resp.StatusCode >= 400 {
		return fmt.Errorf("http %s %s returned %d: %s", method, url, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	return nil
}

func (a *Agent) executeNotifyAction(payload map[string]any) {
	message, _ := payload["message"].(string)
	title, _ := payload["title"].(string)
	if title == "" {
		title = "agentd"
	}
	log.Info("notification", "title", title, "message", message)

	// Try macOS notification
	exec.Command("osascript", "-e", fmt.Sprintf(`display notification "%s" with title "%s"`, message, title)).Run()
}

func (a *Agent) fireStartupEvents() {
	for _, s := range a.skills {
		for _, t := range s.Manifest.Triggers {
			if t.Cron == "@startup" {
				a.events <- watchers.Event{
					Source: "cron",
					Type:   "@startup",
					Payload: map[string]any{
						"skill": s.Manifest.Name,
					},
					Timestamp: time.Now(),
				}
			}
		}
	}
}

func skillNames(ss []skills.Skill) []string {
	names := make([]string, len(ss))
	for i, s := range ss {
		names[i] = s.Manifest.Name
	}
	return names
}

// RunPrompt allows running an ad-hoc prompt against the agent's LLM
func (a *Agent) RunPrompt(ctx context.Context, prompt string) (string, error) {
	if a.llm == nil {
		return "", fmt.Errorf("no LLM configured")
	}

	systemPrompt := BuildSystemPrompt(a.config.Agent.Name, a.skills, a.db)
	resp, err := a.llm.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: systemPrompt,
		Messages: []llm.Message{
			{Role: "user", Content: prompt},
		},
		MaxTokens:   4096,
		Temperature: 0.5,
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
