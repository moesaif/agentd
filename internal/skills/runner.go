package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/log"
)

const defaultTimeout = 30 * time.Second

type RunResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Duration time.Duration
}

func Run(ctx context.Context, skill Skill, event map[string]any, envVars map[string]string) (RunResult, error) {
	timeout := defaultTimeout
	if skill.Manifest.Timeout > 0 {
		timeout = time.Duration(skill.Manifest.Timeout) * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	eventJSON, err := json.Marshal(event)
	if err != nil {
		return RunResult{}, fmt.Errorf("marshaling event: %w", err)
	}

	interpreter := interpreterFor(skill.Path)
	cmd := exec.CommandContext(ctx, interpreter, skill.Path)

	cmd.Env = append(os.Environ(),
		"AGENTD_EVENT="+string(eventJSON),
	)
	for k, v := range envVars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// Check required env vars
	for _, env := range skill.Manifest.Env {
		if os.Getenv(env) == "" {
			if _, ok := envVars[env]; !ok {
				return RunResult{}, fmt.Errorf("required env var %s not set", env)
			}
		}
	}

	start := time.Now()

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	duration := time.Since(start)

	result := RunResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: duration,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return result, fmt.Errorf("running skill %s: %w", skill.Manifest.Name, err)
		}
	}

	log.Debug("skill executed",
		"name", skill.Manifest.Name,
		"exit_code", result.ExitCode,
		"duration", duration,
	)

	return result, nil
}

func interpreterFor(path string) string {
	ext := filepath.Ext(path)
	switch ext {
	case ".py":
		return "python3"
	case ".js":
		return "node"
	case ".rb":
		return "ruby"
	default:
		return "sh"
	}
}
