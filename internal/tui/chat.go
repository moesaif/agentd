package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	agentpkg "github.com/moesaif/agentd/internal/agent"
	"github.com/moesaif/agentd/internal/config"
	"github.com/moesaif/agentd/internal/db"
	"github.com/moesaif/agentd/internal/llm"
	"github.com/moesaif/agentd/internal/skills"
	"github.com/moesaif/agentd/internal/watchers"
)

// ── styles ────────────────────────────────────────────────────────────────────

var (
	statusBarStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)

	statusAccentStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("212")).
				Bold(true).
				Padding(0, 1)

	userBubbleStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("63")).
			Foreground(lipgloss.Color("255")).
			Padding(0, 1).
			MarginBottom(1)

	agentBubbleStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("63")).
				Padding(0, 1).
				MarginBottom(1)

	skillOutputStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), false, false, false, true).
				BorderForeground(lipgloss.Color("240")).
				Foreground(lipgloss.Color("247")).
				PaddingLeft(1).
				MarginBottom(1)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			MarginBottom(1)

	tsStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Italic(true)

	roleStyle = lipgloss.NewStyle().
			Bold(true).
			MarginBottom(0)

	userRoleStyle  = roleStyle.Foreground(lipgloss.Color("212"))
	agentRoleStyle = roleStyle.Foreground(lipgloss.Color("86"))
	skillRoleStyle = roleStyle.Foreground(lipgloss.Color("214"))
	errorRoleStyle = roleStyle.Foreground(lipgloss.Color("196"))

	inputPromptStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("63")).
				Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	dividerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("237"))
)

// ── message types ─────────────────────────────────────────────────────────────

type msgRole string

const (
	roleUser   msgRole = "you"
	roleAgent  msgRole = "agent"
	roleSkill  msgRole = "skill"
	roleSystem msgRole = "system"
	roleError  msgRole = "error"
)

type chatMessage struct {
	role    msgRole
	content string
	ts      time.Time
}

// ── tea messages ──────────────────────────────────────────────────────────────

type llmDoneMsg struct {
	content string
	err     error
}

type skillDoneMsg struct {
	skillName string
	stdout    string
	stderr    string
	exitCode  int
	err       error
}

// ── model ─────────────────────────────────────────────────────────────────────

type ChatModel struct {
	cfg       config.Config
	llmClient llm.Client
	skills    []skills.Skill
	store     *db.DB

	viewport viewport.Model
	input    textinput.Model
	spinner  spinner.Model

	messages []chatMessage
	history  []llm.Message // full conversation sent to LLM

	thinking bool
	ready    bool
	width    int
	height   int
}

func NewChatModel(cfg config.Config, llmClient llm.Client, loadedSkills []skills.Skill, store *db.DB) ChatModel {
	ti := textinput.New()
	ti.Placeholder = "message agentd... (/help for commands)"
	ti.Prompt = ""
	ti.CharLimit = 2048
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))

	m := ChatModel{
		cfg:       cfg,
		llmClient: llmClient,
		skills:    loadedSkills,
		store:     store,
		input:     ti,
		spinner:   sp,
	}

	m.addMessage(roleSystem, fmt.Sprintf(
		"agentd ready — %d skill(s) loaded. Type /help for commands.",
		len(loadedSkills),
	))

	return m
}

func RunChat(cfg config.Config, llmClient llm.Client, loadedSkills []skills.Skill, store *db.DB) error {
	m := NewChatModel(cfg, llmClient, loadedSkills, store)
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run()
	return err
}

// ── bubbletea interface ───────────────────────────────────────────────────────

func (m ChatModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.spinner.Tick)
}

func (m ChatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		headerH := 1
		footerH := 3 // input line + hint + divider
		vpHeight := m.height - headerH - footerH
		if vpHeight < 1 {
			vpHeight = 1
		}
		if !m.ready {
			m.viewport = viewport.New(m.width, vpHeight)
			m.viewport.SetContent(m.renderMessages())
			m.ready = true
		} else {
			m.viewport.Width = m.width
			m.viewport.Height = vpHeight
		}
		m.input.Width = m.width - 4
		m.viewport.GotoBottom()

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			if m.thinking {
				return m, nil
			}
			raw := strings.TrimSpace(m.input.Value())
			if raw == "" {
				return m, nil
			}
			m.input.SetValue("")
			return m.handleInput(raw)
		case "ctrl+l":
			m.messages = nil
			m.history = nil
			m.addMessage(roleSystem, "Chat cleared.")
			m.refreshViewport()
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
		if m.thinking {
			m.refreshViewport()
		}

	case llmDoneMsg:
		m.thinking = false
		if msg.err != nil {
			m.addMessage(roleError, msg.err.Error())
		} else {
			m.addMessage(roleAgent, msg.content)
			m.history = append(m.history, llm.Message{Role: "assistant", Content: msg.content})
		}
		m.refreshViewport()

	case skillDoneMsg:
		m.thinking = false
		if msg.err != nil {
			m.addMessage(roleError, fmt.Sprintf("skill %q failed: %s", msg.skillName, msg.err))
		} else {
			out := strings.TrimSpace(msg.stdout)
			if out == "" {
				out = "(no output)"
			}
			label := fmt.Sprintf("%s  exit %d", msg.skillName, msg.exitCode)
			if msg.stderr != "" {
				out += "\n[stderr] " + strings.TrimSpace(msg.stderr)
			}
			m.addMessage(roleSkill, label+"\n"+out)
		}
		m.refreshViewport()
	}

	// pass keys to viewport for scrolling when not focused on input
	if m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	var inputCmd tea.Cmd
	m.input, inputCmd = m.input.Update(msg)
	cmds = append(cmds, inputCmd)

	return m, tea.Batch(cmds...)
}

func (m ChatModel) View() string {
	if !m.ready {
		return "\n  Initialising..."
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		m.renderStatusBar(),
		m.viewport.View(),
		dividerStyle.Render(strings.Repeat("─", m.width)),
		m.renderInput(),
	)
}

// ── input handling ────────────────────────────────────────────────────────────

func (m ChatModel) handleInput(raw string) (ChatModel, tea.Cmd) {
	if strings.HasPrefix(raw, "/") {
		return m.handleCommand(raw)
	}

	m.addMessage(roleUser, raw)
	m.history = append(m.history, llm.Message{Role: "user", Content: raw})

	if m.llmClient == nil {
		m.addMessage(roleError, "No LLM configured. Run 'agentd init' to set one up.")
		m.refreshViewport()
		return m, nil
	}

	m.thinking = true
	m.refreshViewport()
	return m, m.callLLM()
}

func (m ChatModel) handleCommand(raw string) (ChatModel, tea.Cmd) {
	parts := strings.Fields(raw)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/help":
		m.addMessage(roleSystem, strings.Join([]string{
			"Commands:",
			"  /run <skill>   — manually trigger a skill",
			"  /skills        — list loaded skills",
			"  /clear         — clear the chat",
			"  /help          — show this message",
			"  Ctrl+C         — quit",
			"",
			"Anything else is sent to the agent.",
		}, "\n"))

	case "/skills":
		if len(m.skills) == 0 {
			m.addMessage(roleSystem, "No skills loaded.")
		} else {
			lines := make([]string, 0, len(m.skills)+1)
			lines = append(lines, fmt.Sprintf("%d skill(s) loaded:", len(m.skills)))
			for _, s := range m.skills {
				triggers := formatSkillTriggers(s.Manifest.Triggers)
				lines = append(lines, fmt.Sprintf("  %-24s %s  [%s]", s.Manifest.Name, s.Manifest.Description, triggers))
			}
			m.addMessage(roleSystem, strings.Join(lines, "\n"))
		}

	case "/run":
		if len(parts) < 2 {
			m.addMessage(roleError, "Usage: /run <skill-name>")
			m.refreshViewport()
			return m, nil
		}
		name := parts[1]
		var target *skills.Skill
		for i := range m.skills {
			if m.skills[i].Manifest.Name == name {
				target = &m.skills[i]
				break
			}
		}
		if target == nil {
			m.addMessage(roleError, fmt.Sprintf("skill %q not found. Use /skills to list them.", name))
			m.refreshViewport()
			return m, nil
		}
		m.addMessage(roleSystem, fmt.Sprintf("Running skill: %s", name))
		m.thinking = true
		m.refreshViewport()
		return m, m.runSkill(*target)

	case "/clear":
		m.messages = nil
		m.history = nil
		m.addMessage(roleSystem, "Chat cleared.")

	default:
		m.addMessage(roleError, fmt.Sprintf("Unknown command %q. Type /help for available commands.", cmd))
	}

	m.refreshViewport()
	return m, nil
}

// ── async commands ────────────────────────────────────────────────────────────

func (m ChatModel) callLLM() tea.Cmd {
	systemPrompt := agentpkg.BuildSystemPrompt(m.cfg.Agent.Name, m.skills, m.store)
	history := make([]llm.Message, len(m.history))
	copy(history, m.history)
	client := m.llmClient

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		resp, err := client.Complete(ctx, llm.CompletionRequest{
			SystemPrompt: systemPrompt,
			Messages:     history,
			MaxTokens:    2048,
			Temperature:  0.5,
		})
		if err != nil {
			return llmDoneMsg{err: err}
		}
		return llmDoneMsg{content: resp.Content}
	}
}

func (m ChatModel) runSkill(s skills.Skill) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		event := watchers.Event{
			Source:    "chat",
			Type:      "manual",
			Payload:   map[string]any{"manual": true, "trigger": "chat"},
			Timestamp: time.Now(),
		}
		_ = event // available for future use

		envVars := map[string]string{
			"AGENTD_CONFIG_DIR": cfg.StateDir(),
			"AGENTD_STATE_DIR":  cfg.StateDir(),
		}
		result, err := skills.Run(ctx, s, map[string]any{"manual": true, "trigger": "chat"}, envVars)
		if err != nil {
			return skillDoneMsg{skillName: s.Manifest.Name, err: err}
		}
		return skillDoneMsg{
			skillName: s.Manifest.Name,
			stdout:    result.Stdout,
			stderr:    result.Stderr,
			exitCode:  result.ExitCode,
		}
	}
}

// ── rendering ─────────────────────────────────────────────────────────────────

func (m *ChatModel) addMessage(role msgRole, content string) {
	m.messages = append(m.messages, chatMessage{
		role:    role,
		content: content,
		ts:      time.Now(),
	})
}

func (m *ChatModel) refreshViewport() {
	if !m.ready {
		return
	}
	m.viewport.SetContent(m.renderMessages())
	m.viewport.GotoBottom()
}

func (m ChatModel) renderMessages() string {
	if len(m.messages) == 0 {
		return ""
	}

	maxWidth := m.width - 4
	if maxWidth < 20 {
		maxWidth = 20
	}

	var sb strings.Builder
	for _, msg := range m.messages {
		sb.WriteString(m.renderMessage(msg, maxWidth))
		sb.WriteString("\n")
	}

	if m.thinking {
		sb.WriteString("\n  " + agentRoleStyle.Render("agent") + "  " + m.spinner.View() + " thinking...\n")
	}

	return sb.String()
}

func (m ChatModel) renderMessage(msg chatMessage, maxWidth int) string {
	ts := tsStyle.Render(msg.ts.Format("15:04:05"))

	switch msg.role {
	case roleUser:
		header := userRoleStyle.Render("you") + "  " + ts
		bubble := userBubbleStyle.Width(maxWidth).Render(msg.content)
		return header + "\n" + bubble

	case roleAgent:
		header := agentRoleStyle.Render("agent") + "  " + ts
		bubble := agentBubbleStyle.Width(maxWidth).Render(msg.content)
		return header + "\n" + bubble

	case roleSkill:
		lines := strings.SplitN(msg.content, "\n", 2)
		header := skillRoleStyle.Render("skill: "+lines[0]) + "  " + ts
		body := ""
		if len(lines) > 1 {
			body = "\n" + skillOutputStyle.Width(maxWidth).Render(lines[1])
		}
		return header + body

	case roleError:
		return errorRoleStyle.Render("error") + "  " + ts + "\n" + errorStyle.Render(msg.content)

	default: // system
		return mutedStyle.Render("  "+msg.content)
	}
}

func (m ChatModel) renderStatusBar() string {
	provider := m.cfg.LLM.Provider
	model := m.cfg.LLM.Model
	if provider == "" {
		provider = "no LLM"
	}

	left := statusAccentStyle.Render("agentd")
	mid := statusBarStyle.Render(fmt.Sprintf("%s / %s", provider, model))
	right := statusBarStyle.Render(fmt.Sprintf("%d skills", len(m.skills)))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(mid) - lipgloss.Width(right)
	if gap < 0 {
		gap = 0
	}
	fill := statusBarStyle.Render(strings.Repeat(" ", gap))

	return left + mid + fill + right
}

func (m ChatModel) renderInput() string {
	prompt := inputPromptStyle.Render("›")
	inputView := m.input.View()
	hint := helpStyle.Render("enter send  ↑↓ scroll  ctrl+c quit")
	return fmt.Sprintf("  %s %s\n  %s", prompt, inputView, hint)
}

// ── helpers ───────────────────────────────────────────────────────────────────

func formatSkillTriggers(triggers []skills.Trigger) string {
	var parts []string
	for _, t := range triggers {
		if t.Git != "" {
			parts = append(parts, "git:"+t.Git)
		}
		if t.Filesystem != "" {
			parts = append(parts, "fs")
		}
		if t.Webhook != "" {
			parts = append(parts, "webhook")
		}
		if t.Cron != "" {
			parts = append(parts, "cron:"+t.Cron)
		}
	}
	if len(parts) == 0 {
		return "manual"
	}
	return strings.Join(parts, ", ")
}
