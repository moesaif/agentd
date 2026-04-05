package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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
	"github.com/moesaif/agentd/internal/watchers"
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
		statusCmd(),
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
			fmt.Printf("agentd is running (PID %s)\n\n", pid)

			// Show recent events
			store, err := db.Open(cfg.DBPath())
			if err != nil {
				return err
			}
			defer store.Close()

			events, _ := store.RecentEvents(5)
			if len(events) > 0 {
				fmt.Println("Recent events:")
				for _, e := range events {
					fmt.Printf("  [%s] %s.%s\n", e.CreatedAt.Format("15:04:05"), e.Source, e.Type)
				}
			}

			actions, _ := store.RecentActions(5)
			if len(actions) > 0 {
				fmt.Println("\nRecent actions:")
				for _, a := range actions {
					fmt.Printf("  [%s] %s → %s (%s)\n", a.CreatedAt.Format("15:04:05"), a.SkillName, a.ActionType, a.Status)
				}
			}

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

	runCmd := &cobra.Command{
		Use:   "run <name>",
		Short: "Manually trigger a skill",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadConfig()
			bundledDir := findBundledSkillsDir()
			loaded, _ := skills.LoadAll(cfg.SkillsDir(), bundledDir)

			name := args[0]
			for _, s := range loaded {
				if s.Manifest.Name == name {
					fmt.Printf("Running skill: %s\n", name)
					result, err := skills.Run(context.Background(), s, map[string]any{"manual": true}, nil)
					if err != nil {
						return err
					}
					fmt.Println(result.Stdout)
					if result.Stderr != "" {
						fmt.Fprintf(os.Stderr, "%s", result.Stderr)
					}
					return nil
				}
			}
			return fmt.Errorf("skill not found: %s", name)
		},
	}

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
			fmt.Println("MCP Server Configuration")
			fmt.Println("========================")
			fmt.Printf("Endpoint: http://localhost:%d/mcp\n", cfg.MCP.Port)
			fmt.Println()
			fmt.Println("Add to .vscode/mcp.json or Claude Code config:")
			fmt.Println()
			mcpConfig := map[string]any{
				"mcpServers": map[string]any{
					"agentd": map[string]any{
						"url": fmt.Sprintf("http://localhost:%d/mcp", cfg.MCP.Port),
					},
				},
			}
			data, _ := json.MarshalIndent(mcpConfig, "", "  ")
			fmt.Println(string(data))
		},
	}
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Interactive setup wizard",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.DefaultConfig()

			fmt.Println()
			fmt.Println("  ╔══════════════════════════════════════╗")
			fmt.Println("  ║          Welcome to agentd!          ║")
			fmt.Println("  ║   Your AI agent that acts for you.   ║")
			fmt.Println("  ╚══════════════════════════════════════╝")
			fmt.Println()

			// Check for existing config
			if _, err := os.Stat(cfg.ConfigPath()); err == nil {
				fmt.Println("  Config already exists at", cfg.ConfigPath())
				fmt.Println("  Run 'agentd start' to begin.")
				return nil
			}

			// Check for API key in environment
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
				fmt.Println("    Set ANTHROPIC_API_KEY or OPENAI_API_KEY, then re-run.")
				fmt.Println("    Or configure manually in ~/.agentd/config.yaml")
				fmt.Println()
			}

			// Create directories
			if err := config.EnsureDirs(cfg); err != nil {
				return err
			}
			fmt.Println("  ✓ Created", cfg.StateDir())
			fmt.Println("  ✓ Created", cfg.SkillsDir())

			// Copy bundled skills
			bundledDir := findBundledSkillsDir()
			if bundledDir != "" {
				copied := copyBundledSkills(bundledDir, cfg.SkillsDir())
				if copied > 0 {
					fmt.Printf("  ✓ Installed %d bundled skills\n", copied)
				}
			}

			// Save config
			if err := cfg.Save(cfg.ConfigPath()); err != nil {
				return err
			}
			fmt.Println("  ✓ Config saved to", cfg.ConfigPath())

			fmt.Println()
			fmt.Println("  You're all set! Run 'agentd start' to begin.")
			fmt.Println()
			fmt.Println("  Quick tips:")
			fmt.Println("    agentd start          Start watching (foreground)")
			fmt.Println("    agentd start -d       Start as daemon")
			fmt.Println("    agentd skills         List available skills")
			fmt.Println("    agentd status         Check status")
			fmt.Println()

			return nil
		},
	}
}

func printBanner(cfg config.Config) {
	fmt.Println()
	fmt.Println("  ╔══════════════════════════════════════╗")
	fmt.Println("  ║            agentd running            ║")
	fmt.Println("  ╚══════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("  Provider:  %s (%s)\n", cfg.LLM.Provider, cfg.LLM.Model)
	if cfg.Watchers.Webhook.Enabled {
		fmt.Printf("  Webhook:   http://localhost:%d/webhook\n", cfg.Watchers.Webhook.Port)
	}
	if cfg.MCP.Enabled {
		fmt.Printf("  MCP:       http://localhost:%d/mcp\n", cfg.MCP.Port)
	}
	fmt.Printf("  State:     %s\n", cfg.StateDir())
	fmt.Println()
	fmt.Println("  Press Ctrl+C to stop")
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

