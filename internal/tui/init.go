package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/moesaif/agentd/internal/config"
)

type InitWizardResult struct {
	Config    config.Config
	Cancelled bool
}

type initStep int

const (
	stepIntro initStep = iota
	stepProvider
	stepKeySource
	stepKeyInput
	stepModel
	stepBaseURL
	stepAgentName
	stepMCP
	stepWebhook
	stepReview
)

type menuOption struct {
	Title string
	Desc  string
}

type initModel struct {
	cfg               config.Config
	hasExistingConfig bool
	step              initStep
	cursor            int
	input             textinput.Model
	keyInputRequired  bool
	cancelled         bool
	width             int
	height            int
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205"))

	subtleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("212"))

	boxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 2)
)

func RunInitWizard(cfg config.Config, hasExistingConfig bool) (InitWizardResult, error) {
	m := initModel{
		cfg:               cfg,
		hasExistingConfig: hasExistingConfig,
		step:              stepIntro,
	}

	finalModel, err := tea.NewProgram(m, tea.WithAltScreen()).Run()
	if err != nil {
		return InitWizardResult{}, err
	}

	fm := finalModel.(initModel)
	return InitWizardResult{
		Config:    fm.cfg,
		Cancelled: fm.cancelled,
	}, nil
}

func (m initModel) Init() tea.Cmd {
	return nil
}

func (m initModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			m.cancelled = true
			return m, tea.Quit
		}

		if m.isInputStep() {
			return m.updateInputStep(msg)
		}
		return m.updateMenuStep(msg)
	}

	return m, nil
}

func (m initModel) updateMenuStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	options := m.currentOptions()

	switch msg.String() {
	case "up", "k":
		if len(options) > 0 {
			m.cursor = (m.cursor - 1 + len(options)) % len(options)
		}
	case "down", "j":
		if len(options) > 0 {
			m.cursor = (m.cursor + 1) % len(options)
		}
	case "esc":
		return m.goBack()
	case "enter", " ":
		return m.submitMenu()
	}

	return m, nil
}

func (m initModel) updateInputStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m.goBack()
	case "enter":
		return m.submitInput()
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m initModel) submitMenu() (tea.Model, tea.Cmd) {
	switch m.step {
	case stepIntro:
		return m.setStep(stepProvider)

	case stepProvider:
		switch m.cursor {
		case 0:
			m.setProvider("anthropic")
			return m.setStep(stepKeySource)
		case 1:
			m.setProvider("openai")
			return m.setStep(stepKeySource)
		case 2:
			m.setProvider("openai-compatible")
			return m.setStep(stepKeySource)
		default:
			m.keyInputRequired = false
			m.cfg.LLM.Provider = ""
			m.cfg.LLM.APIKey = ""
			m.cfg.LLM.BaseURL = ""
			return m.setStep(stepAgentName)
		}

	case stepKeySource:
		return m.submitKeySource()

	case stepMCP:
		m.cfg.MCP.Enabled = m.cursor == 0
		return m.setStep(stepWebhook)

	case stepWebhook:
		m.cfg.Watchers.Webhook.Enabled = m.cursor == 0
		return m.setStep(stepReview)

	case stepReview:
		if m.cursor == 0 {
			return m, tea.Quit
		}
		m.cancelled = true
		return m, tea.Quit
	}

	return m, nil
}

func (m initModel) submitKeySource() (tea.Model, tea.Cmd) {
	envVar := envVarForProvider(m.cfg.LLM.Provider)
	envConfigured := envVar != "" && strings.TrimSpace(envValueForProvider(m.cfg.LLM.Provider)) != ""
	currentValue := strings.TrimSpace(m.cfg.LLM.APIKey)

	options := m.keySourceOptions()
	if len(options) == 0 {
		return m.setStep(stepModel)
	}

	selected := options[m.cursor].Title
	switch {
	case strings.HasPrefix(selected, "Use ") && envVar != "":
		m.keyInputRequired = false
		m.cfg.LLM.APIKey = "${" + envVar + "}"
		return m.setStep(stepModel)
	case selected == "Keep current key":
		m.keyInputRequired = false
		if currentValue == "" && envConfigured {
			m.cfg.LLM.APIKey = "${" + envVar + "}"
		}
		return m.setStep(stepModel)
	case selected == "Paste key now":
		m.keyInputRequired = true
		return m.setStep(stepKeyInput)
	default:
		m.keyInputRequired = false
		m.cfg.LLM.APIKey = ""
		return m.setStep(stepModel)
	}
}

func (m initModel) submitInput() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.input.Value())

	switch m.step {
	case stepKeyInput:
		m.keyInputRequired = true
		m.cfg.LLM.APIKey = value
		return m.setStep(stepModel)
	case stepModel:
		if value != "" {
			m.cfg.LLM.Model = value
		}
		if m.cfg.LLM.Provider == "openai" || m.cfg.LLM.Provider == "openai-compatible" {
			return m.setStep(stepBaseURL)
		}
		return m.setStep(stepAgentName)
	case stepBaseURL:
		m.cfg.LLM.BaseURL = value
		return m.setStep(stepAgentName)
	case stepAgentName:
		if value != "" {
			m.cfg.Agent.Name = value
		}
		return m.setStep(stepMCP)
	}

	return m, nil
}

func (m initModel) goBack() (tea.Model, tea.Cmd) {
	switch m.step {
	case stepIntro:
		m.cancelled = true
		return m, tea.Quit
	case stepProvider:
		return m.setStep(stepIntro)
	case stepKeySource:
		return m.setStep(stepProvider)
	case stepKeyInput:
		return m.setStep(stepKeySource)
	case stepModel:
		switch m.cfg.LLM.Provider {
		case "anthropic", "openai", "openai-compatible":
			return m.setStep(stepKeySource)
		default:
			return m.setStep(stepProvider)
		}
	case stepBaseURL:
		return m.setStep(stepModel)
	case stepAgentName:
		if m.cfg.LLM.Provider == "openai" || m.cfg.LLM.Provider == "openai-compatible" {
			return m.setStep(stepBaseURL)
		}
		return m.setStep(stepProvider)
	case stepMCP:
		return m.setStep(stepAgentName)
	case stepWebhook:
		return m.setStep(stepMCP)
	case stepReview:
		return m.setStep(stepWebhook)
	}

	return m, nil
}

func (m initModel) setStep(step initStep) (tea.Model, tea.Cmd) {
	m.step = step
	m.cursor = 0

	if !m.isInputStep() {
		return m, nil
	}

	input := textinput.New()
	input.Prompt = "> "
	input.CharLimit = 512
	input.Width = 56
	input.Focus()
	input.SetValue(m.defaultInputValue())

	switch step {
	case stepKeyInput:
		input.Placeholder = "Paste API key"
		input.EchoMode = textinput.EchoPassword
		input.EchoCharacter = '•'
	case stepModel:
		input.Placeholder = "Model name"
	case stepBaseURL:
		if m.cfg.LLM.Provider == "openai" {
			input.Placeholder = "https://api.openai.com/v1"
		} else {
			input.Placeholder = "http://localhost:11434/v1  (Ollama default)"
		}
	case stepAgentName:
		input.Placeholder = "agentd"
	}

	m.input = input
	return m, textinput.Blink
}

func (m initModel) setProvider(provider string) {
	prev := m.cfg.LLM.Provider
	m.cfg.LLM.Provider = provider

	switch provider {
	case "anthropic":
		if prev != provider {
			m.cfg.LLM.APIKey = ""
			m.keyInputRequired = false
		}
		m.cfg.LLM.BaseURL = ""
		if m.cfg.LLM.Model == "" || strings.HasPrefix(m.cfg.LLM.Model, "gpt-") || m.cfg.LLM.Model == "llama3" {
			m.cfg.LLM.Model = "claude-sonnet-4-5"
		}
	case "openai":
		if prev != provider {
			m.cfg.LLM.APIKey = ""
			m.keyInputRequired = false
		}
		if strings.Contains(m.cfg.LLM.BaseURL, "localhost:11434") {
			m.cfg.LLM.BaseURL = ""
		}
		if m.cfg.LLM.Model == "" || strings.HasPrefix(m.cfg.LLM.Model, "claude-") || m.cfg.LLM.Model == "llama3" {
			m.cfg.LLM.Model = "gpt-4o"
		}
	case "openai-compatible":
		if prev != provider {
			m.cfg.LLM.APIKey = ""
			m.keyInputRequired = false
		}
		if m.cfg.LLM.BaseURL == "" || strings.Contains(m.cfg.LLM.BaseURL, "api.openai.com") || strings.Contains(m.cfg.LLM.BaseURL, "api.anthropic.com") {
			m.cfg.LLM.BaseURL = "http://localhost:11434/v1"
		}
		if m.cfg.LLM.Model == "" || strings.HasPrefix(m.cfg.LLM.Model, "claude-") || strings.HasPrefix(m.cfg.LLM.Model, "gpt-") {
			m.cfg.LLM.Model = "llama3.2"
		}
	}
}

func (m initModel) View() string {
	content := boxStyle.Render(m.viewContent())
	if m.width == 0 || m.width <= lipgloss.Width(content) {
		return content
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

func (m initModel) viewContent() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("agentd setup"))
	b.WriteString("\n")
	b.WriteString(subtleStyle.Render(m.stepLabel()))
	b.WriteString("\n\n")

	switch m.step {
	case stepIntro:
		b.WriteString("A compact terminal wizard to get agentd ready.\n\n")
		if m.hasExistingConfig {
			b.WriteString("Existing config detected. This wizard will update it in place.\n")
		} else {
			b.WriteString("This will create your config, connect an LLM, and keep the next step obvious.\n")
		}
		b.WriteString("\nPress Enter to continue or Ctrl+C to cancel.")
	case stepProvider:
		b.WriteString("Choose how agentd should talk to a model.\n\n")
		b.WriteString(renderOptions(m.currentOptions(), m.cursor))
	case stepKeySource:
		b.WriteString(keySourcePrompt(m.cfg.LLM.Provider))
		b.WriteString("\n\n")
		b.WriteString(renderOptions(m.currentOptions(), m.cursor))
	case stepKeyInput:
		b.WriteString("Paste the API key. It will be hidden while you type.\n\n")
		b.WriteString(m.input.View())
	case stepModel:
		b.WriteString("Pick the model agentd should use.\n\n")
		b.WriteString(m.input.View())
	case stepBaseURL:
		if m.cfg.LLM.Provider == "openai" {
			b.WriteString("Optional base URL. Leave blank to use the official OpenAI API.\n\n")
		} else {
			b.WriteString("Base URL for the OpenAI-compatible endpoint (e.g. Ollama, Groq, Together AI).\n\n")
		}
		b.WriteString(m.input.View())
	case stepAgentName:
		b.WriteString("Choose the local name for this agent instance.\n\n")
		b.WriteString(m.input.View())
	case stepMCP:
		b.WriteString("Expose agentd as an MCP server for editors and tools?\n\n")
		b.WriteString(renderOptions(m.currentOptions(), m.cursor))
	case stepWebhook:
		b.WriteString("Enable the webhook listener for GitHub and GitLab events?\n\n")
		b.WriteString(renderOptions(m.currentOptions(), m.cursor))
	case stepReview:
		b.WriteString("Review the setup before writing config.\n\n")
		b.WriteString(m.reviewSummary())
		b.WriteString("\n")
		b.WriteString(renderOptions(m.currentOptions(), m.cursor))
	}

	b.WriteString("\n\n")
	b.WriteString(subtleStyle.Render("↑/↓ move • Enter select • Esc back • Ctrl+C cancel"))
	return b.String()
}

func (m initModel) stepLabel() string {
	order := m.stepOrder()
	index := 1
	for i, step := range order {
		if step == m.step {
			index = i + 1
			break
		}
	}
	return fmt.Sprintf("Step %d of %d", index, len(order))
}

func (m initModel) stepOrder() []initStep {
	order := []initStep{stepIntro, stepProvider}

	switch m.cfg.LLM.Provider {
	case "anthropic", "openai", "openai-compatible":
		order = append(order, stepKeySource)
		if m.keyInputRequired || m.step == stepKeyInput {
			order = append(order, stepKeyInput)
		}
		order = append(order, stepModel)
		if m.cfg.LLM.Provider == "openai" || m.cfg.LLM.Provider == "openai-compatible" {
			order = append(order, stepBaseURL)
		}
	default:
	}

	order = append(order, stepAgentName, stepMCP, stepWebhook, stepReview)
	return order
}

func (m initModel) currentOptions() []menuOption {
	switch m.step {
	case stepProvider:
		return []menuOption{
			{Title: "Anthropic API", Desc: "Use Claude models with an Anthropic API key"},
			{Title: "OpenAI API", Desc: "Use OpenAI models with an OpenAI API key"},
			{Title: "OpenAI-compatible", Desc: "Ollama, Groq, Together AI, LM Studio, or any compatible endpoint"},
			{Title: "Skip for now", Desc: "Set up the rest of agentd without an LLM"},
		}
	case stepKeySource:
		return m.keySourceOptions()
	case stepMCP, stepWebhook:
		return []menuOption{
			{Title: "Yes", Desc: "Enable it"},
			{Title: "No", Desc: "Disable it"},
		}
	case stepReview:
		return []menuOption{
			{Title: "Save config", Desc: "Write the config and finish setup"},
			{Title: "Cancel setup", Desc: "Exit without writing changes"},
		}
	default:
		return nil
	}
}

func (m initModel) keySourceOptions() []menuOption {
	envVar := envVarForProvider(m.cfg.LLM.Provider)
	envValue := strings.TrimSpace(envValueForProvider(m.cfg.LLM.Provider))
	currentValue := strings.TrimSpace(m.cfg.LLM.APIKey)

	var options []menuOption
	if envVar != "" && envValue != "" {
		options = append(options, menuOption{
			Title: "Use " + envVar,
			Desc:  "Reference the key from the shell environment",
		})
	}
	if currentValue != "" && currentValue != "ollama" {
		options = append(options, menuOption{
			Title: "Keep current key",
			Desc:  "Leave the current key configuration unchanged",
		})
	}
	options = append(options, menuOption{
		Title: "Paste key now",
		Desc:  "Store a key directly in the config file",
	})
	options = append(options, menuOption{
		Title: "Skip for now",
		Desc:  "Finish setup and add the key later",
	})
	return options
}

func (m initModel) reviewSummary() string {
	lines := []string{
		fmt.Sprintf("Provider: %s", providerSummary(m.cfg)),
		fmt.Sprintf("Agent name: %s", m.cfg.Agent.Name),
		fmt.Sprintf("MCP server: %s", enabledLabel(m.cfg.MCP.Enabled)),
		fmt.Sprintf("Webhook listener: %s", enabledLabel(m.cfg.Watchers.Webhook.Enabled)),
	}
	return strings.Join(lines, "\n")
}

func (m initModel) defaultInputValue() string {
	switch m.step {
	case stepKeyInput:
		if strings.HasPrefix(m.cfg.LLM.APIKey, "${") || m.cfg.LLM.APIKey == "ollama" {
			return ""
		}
		return m.cfg.LLM.APIKey
	case stepModel:
		return m.cfg.LLM.Model
	case stepBaseURL:
		return m.cfg.LLM.BaseURL
	case stepAgentName:
		return m.cfg.Agent.Name
	default:
		return ""
	}
}

func (m initModel) isInputStep() bool {
	switch m.step {
	case stepKeyInput, stepModel, stepBaseURL, stepAgentName:
		return true
	default:
		return false
	}
}

func renderOptions(options []menuOption, cursor int) string {
	lines := make([]string, 0, len(options))
	for i, option := range options {
		prefix := "  "
		title := option.Title
		if i == cursor {
			prefix = "› "
			title = selectedStyle.Render(option.Title)
		}
		lines = append(lines, fmt.Sprintf("%s%s", prefix, title))
		if option.Desc != "" {
			lines = append(lines, subtleStyle.Render("  "+option.Desc))
		}
	}
	return strings.Join(lines, "\n")
}

func keySourcePrompt(provider string) string {
	switch provider {
	case "anthropic":
		return "Choose how agentd should get the Anthropic API key."
	case "openai":
		return "Choose how agentd should get the OpenAI API key."
	case "openai-compatible":
		return "Does this endpoint require an API key? (Groq/Together: yes — Ollama: skip)"
	default:
		return "Choose how agentd should get the API key."
	}
}

func providerSummary(cfg config.Config) string {
	switch cfg.LLM.Provider {
	case "anthropic", "openai":
		if cfg.LLM.APIKey == "" {
			return fmt.Sprintf("%s (%s, no key configured)", cfg.LLM.Provider, cfg.LLM.Model)
		}
		if strings.HasPrefix(cfg.LLM.APIKey, "${") {
			return fmt.Sprintf("%s (%s, via env)", cfg.LLM.Provider, cfg.LLM.Model)
		}
		return fmt.Sprintf("%s (%s, stored in config)", cfg.LLM.Provider, cfg.LLM.Model)
	case "openai-compatible":
		keyDesc := "no key"
		if cfg.LLM.APIKey != "" {
			if strings.HasPrefix(cfg.LLM.APIKey, "${") {
				keyDesc = "via env"
			} else {
				keyDesc = "key set"
			}
		}
		return fmt.Sprintf("openai-compatible (%s @ %s, %s)", cfg.LLM.Model, cfg.LLM.BaseURL, keyDesc)
	default:
		return "not configured"
	}
}

func enabledLabel(v bool) string {
	if v {
		return "enabled"
	}
	return "disabled"
}

func envVarForProvider(provider string) string {
	switch provider {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	default:
		return ""
	}
}

func envValueForProvider(provider string) string {
	envVar := envVarForProvider(provider)
	if envVar == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(envVar))
}
