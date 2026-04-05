package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/log"
)

type Skill struct {
	Manifest Manifest
	Path     string
}

func LoadAll(dirs ...string) ([]Skill, error) {
	var skills []Skill

	for _, dir := range dirs {
		loaded, err := loadFromDir(dir)
		if err != nil {
			log.Warn("failed to load skills from directory", "dir", dir, "error", err)
			continue
		}
		skills = append(skills, loaded...)
	}

	return skills, nil
}

func loadFromDir(dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading skills directory: %w", err)
	}

	var skills []Skill
	exts := map[string]bool{".sh": true, ".py": true, ".js": true, ".rb": true, ".go": true}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		ext := filepath.Ext(entry.Name())
		if !exts[ext] {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		content, err := os.ReadFile(path)
		if err != nil {
			log.Warn("failed to read skill file", "path", path, "error", err)
			continue
		}

		manifest, err := ParseManifest(string(content))
		if err != nil {
			log.Debug("skipping file without valid manifest", "path", path, "error", err)
			continue
		}

		skills = append(skills, Skill{
			Manifest: manifest,
			Path:     path,
		})

		log.Debug("loaded skill", "name", manifest.Name, "path", path)
	}

	return skills, nil
}

func FindMatching(skills []Skill, source, eventType string, payload map[string]any) []Skill {
	var matched []Skill

	for _, s := range skills {
		for _, t := range s.Manifest.Triggers {
			if matchesTrigger(t, source, eventType, payload) {
				matched = append(matched, s)
				break
			}
		}
	}

	return matched
}

func matchesTrigger(trigger Trigger, source, eventType string, payload map[string]any) bool {
	switch source {
	case "git":
		return trigger.Git != "" && matchPattern(trigger.Git, eventType)
	case "filesystem":
		if trigger.Filesystem == "" {
			return false
		}
		file, _ := payload["file"].(string)
		return matchFilePattern(trigger.Filesystem, file)
	case "webhook":
		if trigger.Webhook == "" {
			return false
		}
		if trigger.Webhook == "any" {
			return true
		}
		return matchPattern(trigger.Webhook, eventType)
	case "cron":
		return trigger.Cron != "" && (trigger.Cron == eventType || trigger.Cron == "@startup")
	}
	return false
}

func matchPattern(pattern, value string) bool {
	if pattern == "*" || pattern == "any" {
		return true
	}
	return strings.EqualFold(pattern, value)
}

func matchFilePattern(patterns, file string) bool {
	for _, pattern := range strings.Split(patterns, ",") {
		pattern = strings.TrimSpace(pattern)
		matched, err := filepath.Match(pattern, filepath.Base(file))
		if err == nil && matched {
			return true
		}
		// Try matching against full path for ** patterns
		if strings.Contains(pattern, "**") {
			ext := strings.TrimPrefix(pattern, "**/")
			if matched, _ := filepath.Match(ext, filepath.Base(file)); matched {
				return true
			}
		}
	}
	return false
}
