package skills

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Manifest struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	Triggers    []Trigger `yaml:"triggers"`
	Env         []string  `yaml:"env"`
	Model       string    `yaml:"model"`
	Timeout     int       `yaml:"timeout"` // seconds, 0 = default (30s)
}

type Trigger struct {
	Git        string `yaml:"git,omitempty"`
	Filesystem string `yaml:"filesystem,omitempty"`
	Webhook    string `yaml:"webhook,omitempty"`
	Cron       string `yaml:"cron,omitempty"`
}

func ParseManifest(content string) (Manifest, error) {
	var manifest Manifest

	// Find YAML frontmatter between --- markers in comments
	// Supports # comments (bash/python/ruby) and // comments (js/go)
	lines := strings.Split(content, "\n")

	var yamlLines []string
	inFrontmatter := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Strip comment prefix
		stripped := ""
		if strings.HasPrefix(trimmed, "#") {
			stripped = strings.TrimPrefix(trimmed, "#")
			stripped = strings.TrimSpace(stripped)
		} else if strings.HasPrefix(trimmed, "//") {
			stripped = strings.TrimPrefix(trimmed, "//")
			stripped = strings.TrimSpace(stripped)
		}

		if stripped == "---" {
			if inFrontmatter {
				break // closing ---
			}
			inFrontmatter = true
			continue
		}

		if inFrontmatter && stripped != "" {
			yamlLines = append(yamlLines, stripped)
		}
	}

	if len(yamlLines) == 0 {
		return manifest, fmt.Errorf("no YAML frontmatter found")
	}

	yamlContent := strings.Join(yamlLines, "\n")
	if err := yaml.Unmarshal([]byte(yamlContent), &manifest); err != nil {
		return manifest, fmt.Errorf("parsing YAML frontmatter: %w", err)
	}

	if manifest.Name == "" {
		return manifest, fmt.Errorf("skill name is required")
	}

	return manifest, nil
}
