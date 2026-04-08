package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/log"
	"github.com/spf13/cobra"

	"github.com/moesaif/agentd/internal/agent"
	"github.com/moesaif/agentd/internal/config"
	"github.com/moesaif/agentd/internal/db"
	"github.com/moesaif/agentd/internal/llm"
	"github.com/moesaif/agentd/internal/mcp"
	"github.com/moesaif/agentd/internal/skills"
	"github.com/moesaif/agentd/internal/tui"
	"github.com/moesaif/agentd/internal/watchers"
	"github.com/moesaif/agentd/internal/web"
)

var version = "dev"

func main() {
	root := &cobra.Command{
		Use:     "agentd",
		Short:   "Your AI agent that acts. Not one you talk to.",
		Version: version,
	}

	root.AddCommand(
		startCmd(),
		stopCmd(),
		updateCmd(),
		uninstallCmd(),
		statusCmd(),
		chatCmd(),
		webCmd(),
		skillsCmd(),
		historyCmd(),
		memoryCmd(),
		logsCmd(),
		mcpCmd(),
		initCmd(),
	)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

const (
	githubRepoOwner = "moesaif"
	githubRepoName  = "agentd"
)

type githubRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func loadConfig() config.Config {
	home, _ := os.UserHomeDir()
	cfgPath := filepath.Join(home, ".agentd", "config.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Warn("using default config", "error", err)
		cfg = config.DefaultConfig()
	}
	return cfg
}

func startCmd() *cobra.Command {
	var daemon bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the agentd daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			setupLogging(cfg.Agent.LogLevel)

			if err := config.EnsureDirs(cfg); err != nil {
				return fmt.Errorf("creating directories: %w", err)
			}

			if daemon {
				return startDaemon(cfg)
			}

			return startForeground(cfg)
		},
	}
	cmd.Flags().BoolVarP(&daemon, "daemon", "d", false, "Run as background daemon")
	return cmd
}

func startForeground(cfg config.Config) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Open database
	store, err := db.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer store.Close()

	// Create LLM client
	var llmClient llm.Client
	if cfg.LLM.APIKey != "" {
		llmClient, err = llm.NewClient(cfg.LLM)
		if err != nil {
			log.Warn("LLM client not available", "error", err)
		} else {
			log.Info("LLM client initialized", "provider", cfg.LLM.Provider, "model", cfg.LLM.Model)
		}
	} else {
		log.Warn("no LLM API key configured — running in log-only mode")
	}

	// Load skills
	bundledSkillsDir := findBundledSkillsDir()
	loadedSkills, err := skills.LoadAll(cfg.SkillsDir(), bundledSkillsDir)
	if err != nil {
		log.Warn("failed to load skills", "error", err)
	}
	log.Info("skills loaded", "count", len(loadedSkills))

	// Create agent
	a := agent.New(cfg, store, llmClient, loadedSkills)

	// Set up watchers
	cwd, _ := os.Getwd()
	if cfg.Watchers.Filesystem {
		fw, err := watchers.NewFilesystemWatcher(cwd)
		if err != nil {
			log.Warn("filesystem watcher not available", "error", err)
		} else {
			a.AddWatcher(fw)
		}
	}

	if cfg.Watchers.Git {
		gw := watchers.NewGitWatcher(cwd)
		a.AddWatcher(gw)
	}

	if cfg.Watchers.Webhook.Enabled {
		ww := watchers.NewWebhookWatcher(cfg.Watchers.Webhook.Port, cfg.Watchers.Webhook.Secret)
		a.AddWatcher(ww)
	}

	if cfg.Watchers.Cron.Enabled {
		cw := watchers.NewCronWatcher()
		// Register cron triggers from skills
		for _, s := range loadedSkills {
			for _, t := range s.Manifest.Triggers {
				if t.Cron != "" && t.Cron != "@startup" {
					if err := cw.AddSchedule(t.Cron, s.Manifest.Name, a.Events()); err != nil {
						log.Warn("failed to add cron schedule", "skill", s.Manifest.Name, "schedule", t.Cron, "error", err)
					}
				}
			}
		}
		a.AddWatcher(cw)
	}

	// Start MCP server
	if cfg.MCP.Enabled {
		mcpServer := mcp.NewServer(cfg.MCP.Port, store, loadedSkills, a)
		if err := mcpServer.Start(); err != nil {
			log.Warn("MCP server not available", "error", err)
		} else {
			defer mcpServer.Stop()
		}
	}

	// Start agent
	if err := a.Start(ctx); err != nil {
		return fmt.Errorf("starting agent: %w", err)
	}

	// Write PID file
	os.WriteFile(cfg.PIDPath(), []byte(strconv.Itoa(os.Getpid())), 0o644)
	defer os.Remove(cfg.PIDPath())

	printBanner(cfg)

	// Wait for signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Info("shutting down...")
	return a.Stop()
}

func startDaemon(cfg config.Config) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	// Re-execute ourselves without --daemon
	proc, err := os.StartProcess(exe, []string{exe, "start"}, &os.ProcAttr{
		Dir: ".",
		Env: os.Environ(),
		Files: []*os.File{
			os.Stdin,
			os.Stdout,
			os.Stderr,
		},
	})
	if err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}

	fmt.Printf("agentd started (PID %d)\n", proc.Pid)
	proc.Release()
	return nil
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the agentd daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			data, err := os.ReadFile(cfg.PIDPath())
			if err != nil {
				return fmt.Errorf("agentd is not running (no PID file)")
			}

			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				return fmt.Errorf("invalid PID file")
			}

			proc, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("process not found: %w", err)
			}

			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("failed to stop agentd: %w", err)
			}

			fmt.Printf("agentd stopped (PID %d)\n", pid)
			os.Remove(cfg.PIDPath())
			return nil
		},
	}
}

func updateCmd() *cobra.Command {
	var checkOnly bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update agentd to the latest release",
		RunE: func(cmd *cobra.Command, args []string) error {
			release, err := fetchLatestRelease(cmd.Context())
			if err != nil {
				return err
			}

			fmt.Printf("Current version: %s\n", version)
			fmt.Printf("Latest version:  %s\n", release.TagName)

			if version != "dev" && version == release.TagName {
				fmt.Println("agentd is already up to date.")
				return nil
			}

			if checkOnly {
				return nil
			}

			assetName := releaseAssetName()
			downloadURL := releaseAssetURL(release, assetName)
			if downloadURL == "" {
				return fmt.Errorf("no release asset found for %s", assetName)
			}

			exePath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locating current executable: %w", err)
			}
			exePath, err = filepath.EvalSymlinks(exePath)
			if err != nil {
				// If symlink resolution fails, still try the raw executable path.
				exePath, _ = os.Executable()
			}

			if runtime.GOOS == "windows" {
				targetPath := exePath + ".new"
				if err := downloadBinary(cmd.Context(), downloadURL, targetPath); err != nil {
					return err
				}
				fmt.Printf("Downloaded update to %s\n", targetPath)
				fmt.Println("Windows cannot replace a running binary in-place.")
				fmt.Println("Close agentd and replace the existing executable with the .new file.")
				return nil
			}

			tmpPath := exePath + ".tmp"
			if err := downloadBinary(cmd.Context(), downloadURL, tmpPath); err != nil {
				return err
			}
			defer os.Remove(tmpPath)

			if err := os.Rename(tmpPath, exePath); err != nil {
				return fmt.Errorf("replacing %s failed: %w\nTry re-running with elevated permissions or reinstall via the install script", exePath, err)
			}

			fmt.Printf("Updated agentd to %s\n", release.TagName)
			return nil
		},
	}

	cmd.Flags().BoolVar(&checkOnly, "check", false, "Only check whether an update is available")
	return cmd
}

func uninstallCmd() *cobra.Command {
	var assumeYes bool
	var keepState bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove agentd from this machine",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()

			if !assumeYes {
				fmt.Println("This will remove the agentd executable.")
				if keepState {
					fmt.Println("Your state directory will be kept.")
				} else {
					fmt.Printf("Your state directory will be removed: %s\n", cfg.StateDir())
				}

				if isInteractiveSession() {
					reader := bufio.NewReader(os.Stdin)
					ok, err := promptYesNo(reader, "Continue", false)
					if err != nil {
						return err
					}
					if !ok {
						fmt.Println("Uninstall cancelled.")
						return nil
					}
				} else {
					return fmt.Errorf("refusing to uninstall without confirmation; re-run with --yes")
				}
			}

			_ = stopAgentProcess(cfg)

			if !keepState {
				if err := os.RemoveAll(cfg.StateDir()); err != nil {
					return fmt.Errorf("removing state directory %s: %w", cfg.StateDir(), err)
				}
				fmt.Printf("Removed state directory %s\n", cfg.StateDir())
			}

			exePath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("locating current executable: %w", err)
			}
			exePath, err = filepath.EvalSymlinks(exePath)
			if err != nil {
				exePath, _ = os.Executable()
			}

			if runtime.GOOS == "windows" {
				fmt.Printf("agentd executable is at %s\n", exePath)
				fmt.Println("Windows does not allow a running process to delete itself.")
				fmt.Println("Delete the executable manually after this command exits.")
				return nil
			}

			if err := os.Remove(exePath); err != nil {
				return fmt.Errorf("removing executable %s failed: %w\nTry re-running with elevated permissions", exePath, err)
			}

			fmt.Printf("Removed executable %s\n", exePath)
			fmt.Println("agentd has been uninstalled.")
			return nil
		},
	}

	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "Skip confirmation")
	cmd.Flags().BoolVar(&keepState, "keep-state", false, "Keep ~/.agentd state and config files")
	return cmd
}

func webCmd() *cobra.Command {
	var port int

	cmd := &cobra.Command{
		Use:   "web",
		Short: "Start the web UI",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			setupLogging(cfg.Agent.LogLevel)

			if err := config.EnsureDirs(cfg); err != nil {
				return err
			}

			store, err := db.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer store.Close()

			var llmClient llm.Client
			if cfg.LLM.APIKey != "" {
				llmClient, err = llm.NewClient(cfg.LLM)
				if err != nil {
					log.Warn("LLM unavailable", "error", err)
				}
			}

			bundledDir := findBundledSkillsDir()
			loadedSkills, _ := skills.LoadAll(cfg.SkillsDir(), bundledDir)

			srv := web.New(port, cfg, store, llmClient, loadedSkills)
			if err := srv.Start(); err != nil {
				return fmt.Errorf("starting web server: %w", err)
			}

			fmt.Println(tui.RenderKeyValueCard("agentd web UI", []tui.KeyValue{
				{Label: "URL", Value: fmt.Sprintf("http://localhost:%d", port)},
				{Label: "Provider", Value: describeProvider(cfg)},
				{Label: "Skills", Value: fmt.Sprintf("%d loaded", len(loadedSkills))},
			}))
			fmt.Println(tui.Muted("Press Ctrl+C to stop"))

			sig := make(chan os.Signal, 1)
			signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
			<-sig

			srv.Stop()
			return nil
		},
	}
	cmd.Flags().IntVarP(&port, "port", "p", 7779, "Port to listen on")
	return cmd
}

func chatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "chat",
		Short: "Interactive chat with agentd",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			setupLogging("error") // suppress logs inside the TUI

			if err := config.EnsureDirs(cfg); err != nil {
				return err
			}

			store, err := db.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("opening database: %w", err)
			}
			defer store.Close()

			var llmClient llm.Client
			if cfg.LLM.APIKey != "" {
				llmClient, err = llm.NewClient(cfg.LLM)
				if err != nil {
					log.Warn("LLM unavailable", "error", err)
				}
			}

			bundledDir := findBundledSkillsDir()
			loadedSkills, _ := skills.LoadAll(cfg.SkillsDir(), bundledDir)

			return tui.RunChat(cfg, llmClient, loadedSkills, store)
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show daemon status and recent events",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()

			// Check if running
			data, err := os.ReadFile(cfg.PIDPath())
			if err != nil {
				fmt.Println("agentd is not running")
				return nil
			}

			pid := strings.TrimSpace(string(data))

			// Show recent events
			store, err := db.Open(cfg.DBPath())
			if err != nil {
				return err
			}
			defer store.Close()

			events, _ := store.RecentEvents(5)
			var eventLines []string
			for _, e := range events {
				eventLines = append(eventLines, fmt.Sprintf("[%s] %s.%s", e.CreatedAt.Format("15:04:05"), e.Source, e.Type))
			}

			actions, _ := store.RecentActions(5)
			var actionLines []string
			for _, a := range actions {
				actionLines = append(actionLines, fmt.Sprintf("[%s] %s -> %s (%s)", a.CreatedAt.Format("15:04:05"), a.SkillName, a.ActionType, a.Status))
			}

			fmt.Println(tui.RenderStack(
				tui.RenderKeyValueCard("agentd status", []tui.KeyValue{
					{Label: "Status", Value: "running"},
					{Label: "PID", Value: pid},
					{Label: "State", Value: cfg.StateDir()},
				}),
				tui.RenderListCard("Recent events", eventLines),
				tui.RenderListCard("Recent actions", actionLines),
			))

			return nil
		},
	}
}

func skillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage skills",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all loaded skills",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			bundledDir := findBundledSkillsDir()
			loaded, _ := skills.LoadAll(cfg.SkillsDir(), bundledDir)

			if len(loaded) == 0 {
				fmt.Println("No skills loaded.")
				fmt.Printf("Add skills to %s\n", cfg.SkillsDir())
				return nil
			}

			fmt.Printf("%-25s %-50s %s\n", "NAME", "DESCRIPTION", "TRIGGERS")
			fmt.Println(strings.Repeat("-", 100))
			for _, s := range loaded {
				triggers := formatTriggers(s.Manifest.Triggers)
				fmt.Printf("%-25s %-50s %s\n", s.Manifest.Name, truncate(s.Manifest.Description, 48), triggers)
			}
			return nil
		},
	}

	var payloadFlag string
	var withLLM bool

	runCmd := &cobra.Command{
		Use:   "run <name>",
		Short: "Manually trigger a skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			bundledDir := findBundledSkillsDir()
			loaded, _ := skills.LoadAll(cfg.SkillsDir(), bundledDir)

			name := args[0]
			var target *skills.Skill
			for i := range loaded {
				if loaded[i].Manifest.Name == name {
					target = &loaded[i]
					break
				}
			}
			if target == nil {
				return fmt.Errorf("skill not found: %s", name)
			}

			payload := map[string]any{"manual": true, "trigger": "manual"}
			if payloadFlag != "" {
				var extra map[string]any
				if err := json.Unmarshal([]byte(payloadFlag), &extra); err != nil {
					return fmt.Errorf("--payload must be valid JSON: %w", err)
				}
				for k, v := range extra {
					payload[k] = v
				}
			}

			envVars := map[string]string{
				"AGENTD_CONFIG_DIR": cfg.StateDir(),
				"AGENTD_STATE_DIR":  cfg.StateDir(),
			}
			result, err := skills.Run(cmd.Context(), *target, payload, envVars)
			if err != nil {
				return err
			}

			exitStatus := "0"
			if result.ExitCode != 0 {
				exitStatus = fmt.Sprintf("%d (failed)", result.ExitCode)
			}
			fmt.Println(tui.RenderKeyValueCard("Result", []tui.KeyValue{
				{Label: "Skill",     Value: target.Manifest.Name},
				{Label: "Exit code", Value: exitStatus},
				{Label: "Duration",  Value: result.Duration.Round(time.Millisecond).String()},
			}))

			if result.Stdout != "" {
				fmt.Println(tui.RenderCodeCard("Output", strings.TrimRight(result.Stdout, "\n")))
			}
			if result.Stderr != "" {
				fmt.Println(tui.RenderCodeCard("Stderr", strings.TrimRight(result.Stderr, "\n")))
			}

			if withLLM {
				if cfg.LLM.APIKey == "" {
					fmt.Println(tui.Muted("--llm: no API key configured, skipping"))
					return nil
				}
				llmClient, err := llm.NewClient(cfg.LLM)
				if err != nil {
					return fmt.Errorf("initializing LLM: %w", err)
				}
				store, err := db.Open(cfg.DBPath())
				if err != nil {
					return fmt.Errorf("opening database: %w", err)
				}
				defer store.Close()

				event := watchers.Event{
					Source:    "manual",
					Type:      "run",
					Payload:   payload,
					Timestamp: time.Now(),
				}
				systemPrompt := agent.BuildSystemPrompt(cfg.Agent.Name, loaded, store)
				userMessage := agent.BuildEventMessage(event, result.Stdout)
				resp, err := llmClient.Complete(cmd.Context(), llm.CompletionRequest{
					SystemPrompt: systemPrompt,
					Messages:     []llm.Message{{Role: "user", Content: userMessage}},
					MaxTokens:    2048,
					Temperature:  0.3,
				})
				if err != nil {
					return fmt.Errorf("LLM call failed: %w", err)
				}
				fmt.Println(tui.RenderCodeCard("LLM response", resp.Content))
			}

			return nil
		},
	}
	runCmd.Flags().StringVar(&payloadFlag, "payload", "", `JSON payload merged into the event (e.g. '{"file":"main.go"}')`)
	runCmd.Flags().BoolVar(&withLLM, "llm", false, "Pass skill output through the LLM and show the response")

	cmd.AddCommand(listCmd, runCmd)

	// Make "agentd skills" (no subcommand) default to list
	cmd.RunE = listCmd.RunE

	return cmd
}

func historyCmd() *cobra.Command {
	var limit int

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show recent events and actions",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			store, err := db.Open(cfg.DBPath())
			if err != nil {
				return err
			}
			defer store.Close()

			events, _ := store.RecentEvents(limit)
			actions, _ := store.RecentActions(limit)

			fmt.Println("Events:")
			if len(events) == 0 {
				fmt.Println("  (none)")
			}
			for _, e := range events {
				payloadStr := ""
				if data, err := json.Marshal(e.Payload); err == nil {
					payloadStr = string(data)
				}
				fmt.Printf("  #%d [%s] %s.%s %s\n", e.ID, e.CreatedAt.Format("2006-01-02 15:04:05"), e.Source, e.Type, truncate(payloadStr, 60))
			}

			fmt.Println("\nActions:")
			if len(actions) == 0 {
				fmt.Println("  (none)")
			}
			for _, a := range actions {
				fmt.Printf("  #%d [%s] %s → %s (%s)\n", a.ID, a.CreatedAt.Format("2006-01-02 15:04:05"), a.SkillName, a.ActionType, a.Status)
			}

			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Number of items to show")
	return cmd
}

func memoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Manage persistent memory",
	}

	getCmd := &cobra.Command{
		Use:   "get <key>",
		Short: "Read from persistent memory",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			store, err := db.Open(cfg.DBPath())
			if err != nil {
				return err
			}
			defer store.Close()

			val, err := store.GetMemory(args[0])
			if err != nil {
				return err
			}
			if val == "" {
				fmt.Printf("(not set)\n")
			} else {
				fmt.Println(val)
			}
			return nil
		},
	}

	setCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Write to persistent memory",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			store, err := db.Open(cfg.DBPath())
			if err != nil {
				return err
			}
			defer store.Close()

			value := strings.Join(args[1:], " ")
			return store.SetMemory(args[0], value)
		},
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all memory keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			store, err := db.Open(cfg.DBPath())
			if err != nil {
				return err
			}
			defer store.Close()

			mem, err := store.AllMemory()
			if err != nil {
				return err
			}
			if len(mem) == 0 {
				fmt.Println("(empty)")
				return nil
			}
			for k, v := range mem {
				fmt.Printf("  %s = %s\n", k, truncate(v, 60))
			}
			return nil
		},
	}

	cmd.AddCommand(getCmd, setCmd, listCmd)
	return cmd
}

func logsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Tail the agentd log",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			logPath := cfg.LogPath()
			if _, err := os.Stat(logPath); os.IsNotExist(err) {
				fmt.Println("No logs yet.")
				return nil
			}
			tailCmd := exec.Command("tail", "-f", logPath)
			tailCmd.Stdout = os.Stdout
			tailCmd.Stderr = os.Stderr
			return tailCmd.Run()
		},
	}
}

func mcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Show MCP server connection info",
		Run: func(cmd *cobra.Command, args []string) {
			cfg := loadConfig()
			mcpConfig := map[string]any{
				"mcpServers": map[string]any{
					"agentd": map[string]any{
						"url": fmt.Sprintf("http://localhost:%d/mcp", cfg.MCP.Port),
					},
				},
			}
			data, _ := json.MarshalIndent(mcpConfig, "", "  ")
			fmt.Println(tui.RenderStack(
				tui.RenderKeyValueCard("MCP server", []tui.KeyValue{
					{Label: "Enabled", Value: fmt.Sprintf("%t", cfg.MCP.Enabled)},
					{Label: "Endpoint", Value: fmt.Sprintf("http://localhost:%d/mcp", cfg.MCP.Port)},
				}),
				tui.RenderCodeCard("Editor config", string(data)),
			))
		},
	}
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Interactive setup wizard",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println()
			fmt.Println("  ╔══════════════════════════════════════╗")
			fmt.Println("  ║          Welcome to agentd!          ║")
			fmt.Println("  ║   Your AI agent that acts for you.   ║")
			fmt.Println("  ╚══════════════════════════════════════╝")
			fmt.Println()

			if !isInteractiveSession() {
				return runNonInteractiveInit()
			}

			return runInteractiveInit()
		},
	}
}

func runNonInteractiveInit() error {
	cfg := config.DefaultConfig()
	if _, err := os.Stat(cfg.ConfigPath()); err == nil {
		fmt.Println("  Config already exists at", cfg.ConfigPath())
		fmt.Println("  Run 'agentd init' in a terminal to reconfigure, or edit the file manually.")
		return nil
	}

	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		cfg.LLM.Provider = "anthropic"
		cfg.LLM.APIKey = "${ANTHROPIC_API_KEY}"
		fmt.Println("  ✓ Found ANTHROPIC_API_KEY in environment")
	} else if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		cfg.LLM.Provider = "openai"
		cfg.LLM.APIKey = "${OPENAI_API_KEY}"
		cfg.LLM.Model = "gpt-4o"
		fmt.Println("  ✓ Found OPENAI_API_KEY in environment")
	} else {
		fmt.Println("  ! No API key found in environment.")
		fmt.Println("    Run 'agentd init' in an interactive terminal to set one now.")
		fmt.Println("    Or configure ~/.agentd/config.yaml manually.")
		fmt.Println()
	}

	if err := config.EnsureDirs(cfg); err != nil {
		return err
	}
	fmt.Println("  ✓ Created", cfg.StateDir())
	fmt.Println("  ✓ Created", cfg.SkillsDir())

	bundledDir := findBundledSkillsDir()
	if bundledDir != "" {
		copied := copyBundledSkills(bundledDir, cfg.SkillsDir())
		if copied > 0 {
			fmt.Printf("  ✓ Installed %d bundled skills\n", copied)
		}
	}

	if err := cfg.Save(cfg.ConfigPath()); err != nil {
		return err
	}
	fmt.Println("  ✓ Config saved to", cfg.ConfigPath())
	fmt.Println()
	fmt.Println("  You're all set! Run 'agentd start' to begin.")
	return nil
}

func runInteractiveInit() error {
	cfg := config.DefaultConfig()
	cfgPath := cfg.ConfigPath()
	hasExistingConfig := false

	if _, err := os.Stat(cfgPath); err == nil {
		hasExistingConfig = true
		existing, loadErr := config.Load(cfgPath)
		if loadErr == nil {
			cfg = existing
		}
	}

	if err := config.EnsureDirs(cfg); err != nil {
		return err
	}

	result, err := tui.RunInitWizard(cfg, hasExistingConfig)
	if err != nil {
		return err
	}
	if result.Cancelled {
		fmt.Println("  Setup cancelled.")
		return nil
	}
	cfg = result.Config

	bundledDir := findBundledSkillsDir()
	if bundledDir != "" {
		copied := copyBundledSkills(bundledDir, cfg.SkillsDir())
		if copied > 0 {
			fmt.Printf("  ✓ Installed %d bundled skills\n", copied)
		} else {
			fmt.Println("  ✓ Bundled skills already present")
		}
	}

	if err := cfg.Save(cfgPath); err != nil {
		return err
	}

	setupCards := []string{
		tui.RenderKeyValueCard("Setup complete", []tui.KeyValue{
			{Label: "Config", Value: cfgPath},
			{Label: "Provider", Value: describeProvider(cfg)},
			{Label: "MCP", Value: enabledValue(cfg.MCP.Enabled, fmt.Sprintf("http://localhost:%d/mcp", cfg.MCP.Port))},
			{Label: "Webhook", Value: enabledValue(cfg.Watchers.Webhook.Enabled, fmt.Sprintf("http://localhost:%d/webhook", cfg.Watchers.Webhook.Port))},
		}),
		tui.RenderListCard("Next steps", []string{
			"agentd start      Start watching in the foreground",
			"agentd start -d   Start as a background process",
			"agentd skills     See what is installed",
			"agentd status     Check recent events and actions",
		}),
	}
	if needsAPIKeyHint(cfg) {
		setupCards = append(setupCards, tui.RenderListCard("Credential hint", []string{
			"No API key configured yet.",
			apiKeyHint(cfg),
		}))
	}
	fmt.Println()
	fmt.Println(tui.RenderStack(setupCards...))

	return nil
}

func isInteractiveSession() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func promptYesNo(reader *bufio.Reader, label string, defaultYes bool) (bool, error) {
	defaultValue := "y/N"
	if defaultYes {
		defaultValue = "Y/n"
	}
	for {
		fmt.Printf("%s [%s]: ", label, defaultValue)
		input, err := reader.ReadString('\n')
		if err != nil {
			return false, err
		}
		input = strings.ToLower(strings.TrimSpace(input))
		if input == "" {
			return defaultYes, nil
		}
		if input == "y" || input == "yes" {
			return true, nil
		}
		if input == "n" || input == "no" {
			return false, nil
		}
		fmt.Println("  Enter y or n.")
	}
}

func describeProvider(cfg config.Config) string {
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
		base := cfg.LLM.BaseURL
		if base == "" {
			base = "http://localhost:11434/v1"
		}
		return fmt.Sprintf("openai-compatible (%s @ %s)", cfg.LLM.Model, base)
	default:
		return "not configured"
	}
}

func needsAPIKeyHint(cfg config.Config) bool {
	return (cfg.LLM.Provider == "anthropic" || cfg.LLM.Provider == "openai") && cfg.LLM.APIKey == ""
}

func apiKeyHint(cfg config.Config) string {
	switch cfg.LLM.Provider {
	case "anthropic":
		return "export ANTHROPIC_API_KEY=your-key"
	case "openai":
		return "export OPENAI_API_KEY=your-key"
	default:
		return ""
	}
}

func enabledValue(enabled bool, value string) string {
	if enabled {
		return value
	}
	return "disabled"
}

func fetchLatestRelease(ctx context.Context) (githubRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", githubRepoOwner, githubRepoName), nil)
	if err != nil {
		return githubRelease{}, fmt.Errorf("creating release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "agentd/"+version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return githubRelease{}, fmt.Errorf("fetching latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return githubRelease{}, fmt.Errorf("GitHub API returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return githubRelease{}, fmt.Errorf("decoding release metadata: %w", err)
	}
	if release.TagName == "" {
		return githubRelease{}, fmt.Errorf("latest release metadata was missing a tag name")
	}
	return release, nil
}

func releaseAssetName() string {
	name := fmt.Sprintf("agentd-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func releaseAssetURL(release githubRelease, assetName string) string {
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			return asset.BrowserDownloadURL
		}
	}
	return ""
}

func downloadBinary(ctx context.Context, url, targetPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("creating download request: %w", err)
	}
	req.Header.Set("User-Agent", "agentd/"+version)

	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading update: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("download failed with %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("creating %s: %w", targetPath, err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", targetPath, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", targetPath, err)
	}

	return nil
}

func stopAgentProcess(cfg config.Config) error {
	data, err := os.ReadFile(cfg.PIDPath())
	if err != nil {
		return nil
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return nil
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return nil
	}

	_ = os.Remove(cfg.PIDPath())
	return nil
}

func printBanner(cfg config.Config) {
	fmt.Println()
	fmt.Println(tui.RenderKeyValueCard("agentd running", []tui.KeyValue{
		{Label: "Provider", Value: fmt.Sprintf("%s (%s)", cfg.LLM.Provider, cfg.LLM.Model)},
		{Label: "Webhook", Value: enabledValue(cfg.Watchers.Webhook.Enabled, fmt.Sprintf("http://localhost:%d/webhook", cfg.Watchers.Webhook.Port))},
		{Label: "MCP", Value: enabledValue(cfg.MCP.Enabled, fmt.Sprintf("http://localhost:%d/mcp", cfg.MCP.Port))},
		{Label: "State", Value: cfg.StateDir()},
	}))
	fmt.Println(tui.Muted("Press Ctrl+C to stop"))
	fmt.Println()
}

func setupLogging(level string) {
	switch level {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	default:
		log.SetLevel(log.InfoLevel)
	}
	log.SetTimeFormat(time.Kitchen)
}

func findBundledSkillsDir() string {
	// Check relative to executable
	exe, err := os.Executable()
	if err == nil {
		dir := filepath.Join(filepath.Dir(exe), "..", "skills")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
	}

	// Check relative to cwd (for development)
	if info, err := os.Stat("skills"); err == nil && info.IsDir() {
		return "skills"
	}

	return ""
}

func copyBundledSkills(src, dst string) int {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0
	}

	copied := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		// Don't overwrite existing skills
		if _, err := os.Stat(dstPath); err == nil {
			continue
		}

		data, err := os.ReadFile(srcPath)
		if err != nil {
			continue
		}
		if err := os.WriteFile(dstPath, data, 0o755); err != nil {
			continue
		}
		copied++
	}
	return copied
}

func formatTriggers(triggers []skills.Trigger) string {
	var parts []string
	for _, t := range triggers {
		if t.Git != "" {
			parts = append(parts, "git:"+t.Git)
		}
		if t.Filesystem != "" {
			parts = append(parts, "fs:"+t.Filesystem)
		}
		if t.Webhook != "" {
			parts = append(parts, "wh:"+t.Webhook)
		}
		if t.Cron != "" {
			parts = append(parts, "cron:"+t.Cron)
		}
	}
	return strings.Join(parts, ", ")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
