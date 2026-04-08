package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	agentpkg "github.com/moesaif/agentd/internal/agent"
	"github.com/moesaif/agentd/internal/config"
	"github.com/moesaif/agentd/internal/db"
	"github.com/moesaif/agentd/internal/llm"
	"github.com/moesaif/agentd/internal/skills"
)

//go:embed static
var staticFiles embed.FS

// Server serves the agentd web UI and REST API.
type Server struct {
	cfg       config.Config
	store     *db.DB
	llmClient llm.Client
	skills    []skills.Skill
	srv       *http.Server
}

func New(port int, cfg config.Config, store *db.DB, llmClient llm.Client, loadedSkills []skills.Skill) *Server {
	s := &Server{
		cfg:       cfg,
		store:     store,
		llmClient: llmClient,
		skills:    loadedSkills,
	}

	mux := http.NewServeMux()

	// API
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/skills", s.handleSkills)
	mux.HandleFunc("/api/skills/run", s.handleRunSkill)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/memory", s.handleMemory)
	mux.HandleFunc("/api/chat", s.handleChat)

	// Static frontend — must be last so /api/* routes take precedence
	static, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(static)))

	s.srv = &http.Server{
		Addr:         fmt.Sprintf(":%d", port),
		Handler:      cors(mux),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	return s
}

func (s *Server) Start() error {
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("web server error", "error", err)
		}
	}()
	return nil
}

func (s *Server) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

// ── handlers ──────────────────────────────────────────────────────────────────

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"agent_name":   s.cfg.Agent.Name,
		"provider":     s.cfg.LLM.Provider,
		"model":        s.cfg.LLM.Model,
		"skills_count": len(s.skills),
		"llm_ready":    s.llmClient != nil,
	})
}

func (s *Server) handleSkills(w http.ResponseWriter, r *http.Request) {
	type skillInfo struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Triggers    []string `json:"triggers"`
	}

	result := make([]skillInfo, 0, len(s.skills))
	for _, sk := range s.skills {
		var triggers []string
		for _, t := range sk.Manifest.Triggers {
			if t.Git != "" {
				triggers = append(triggers, "git:"+t.Git)
			}
			if t.Filesystem != "" {
				triggers = append(triggers, "fs:"+t.Filesystem)
			}
			if t.Webhook != "" {
				triggers = append(triggers, "webhook:"+t.Webhook)
			}
			if t.Cron != "" {
				triggers = append(triggers, "cron:"+t.Cron)
			}
		}
		result = append(result, skillInfo{
			Name:        sk.Manifest.Name,
			Description: sk.Manifest.Description,
			Triggers:    triggers,
		})
	}

	writeJSON(w, result)
}

func (s *Server) handleRunSkill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name    string         `json:"name"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var target *skills.Skill
	for i := range s.skills {
		if s.skills[i].Manifest.Name == req.Name {
			target = &s.skills[i]
			break
		}
	}
	if target == nil {
		writeError(w, fmt.Sprintf("skill not found: %s", req.Name), http.StatusNotFound)
		return
	}

	payload := map[string]any{"manual": true, "trigger": "web"}
	for k, v := range req.Payload {
		payload[k] = v
	}

	envVars := map[string]string{
		"AGENTD_CONFIG_DIR": s.cfg.StateDir(),
		"AGENTD_STATE_DIR":  s.cfg.StateDir(),
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	result, err := skills.Run(ctx, *target, payload, envVars)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"stdout":      result.Stdout,
		"stderr":      result.Stderr,
		"exit_code":   result.ExitCode,
		"duration_ms": result.Duration.Milliseconds(),
	})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.RecentEvents(20)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	actions, err := s.store.RecentActions(20)
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	type eventRow struct {
		ID        int64          `json:"id"`
		Source    string         `json:"source"`
		Type      string         `json:"type"`
		Payload   map[string]any `json:"payload"`
		CreatedAt string         `json:"created_at"`
	}
	type actionRow struct {
		ID         int64  `json:"id"`
		SkillName  string `json:"skill_name"`
		ActionType string `json:"action_type"`
		Status     string `json:"status"`
		CreatedAt  string `json:"created_at"`
	}

	evRows := make([]eventRow, len(events))
	for i, e := range events {
		evRows[i] = eventRow{e.ID, e.Source, e.Type, e.Payload, e.CreatedAt.Format(time.RFC3339)}
	}
	acRows := make([]actionRow, len(actions))
	for i, a := range actions {
		acRows[i] = actionRow{a.ID, a.SkillName, a.ActionType, a.Status, a.CreatedAt.Format(time.RFC3339)}
	}

	writeJSON(w, map[string]any{"events": evRows, "actions": acRows})
}

func (s *Server) handleMemory(w http.ResponseWriter, r *http.Request) {
	mem, err := s.store.AllMemory()
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, mem)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if s.llmClient == nil {
		writeError(w, "no LLM configured — run 'agentd init' to set one up", http.StatusServiceUnavailable)
		return
	}

	var req struct {
		Messages []llm.Message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, "messages must not be empty", http.StatusBadRequest)
		return
	}

	systemPrompt := agentpkg.BuildSystemPrompt(s.cfg.Agent.Name, s.skills, s.store)

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	resp, err := s.llmClient.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: systemPrompt,
		Messages:     req.Messages,
		MaxTokens:    2048,
		Temperature:  0.5,
	})
	if err != nil {
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{"content": resp.Content})
}

// ── helpers ───────────────────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Strip trailing slash for API routes so /api/skills/ == /api/skills
		if strings.HasPrefix(r.URL.Path, "/api/") {
			r.URL.Path = strings.TrimRight(r.URL.Path, "/")
		}
		next.ServeHTTP(w, r)
	})
}
