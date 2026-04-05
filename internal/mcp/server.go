package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"
	"github.com/moesaif/agentd/internal/db"
	"github.com/moesaif/agentd/internal/skills"
)

// AgentRunner provides the agent's RunPrompt capability to the MCP server
type AgentRunner interface {
	RunPrompt(ctx context.Context, prompt string) (string, error)
}

type Server struct {
	port    int
	db      *db.DB
	skills  []skills.Skill
	agent   AgentRunner
	server  *http.Server
	reqID   atomic.Int64
}

func NewServer(port int, store *db.DB, loadedSkills []skills.Skill, agent AgentRunner) *Server {
	return &Server{
		port:   port,
		db:     store,
		skills: loadedSkills,
		agent:  agent,
	}
}

func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	s.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", s.port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	go func() {
		log.Info("MCP server started", "port", s.port)
		if err := s.server.ListenAndServe(); err != http.ErrServerClosed {
			log.Error("MCP server error", "error", err)
		}
	}()

	return nil
}

func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.server.Shutdown(ctx)
}

// JSON-RPC types
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Result  any    `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req jsonRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, nil, -32700, "parse error")
		return
	}

	w.Header().Set("Content-Type", "application/json")

	switch req.Method {
	case "initialize":
		s.handleInitialize(w, req)
	case "tools/list":
		s.handleToolsList(w, req)
	case "tools/call":
		s.handleToolsCall(w, r.Context(), req)
	default:
		writeError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func (s *Server) handleInitialize(w http.ResponseWriter, req jsonRPCRequest) {
	writeResult(w, req.ID, map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]any{
			"tools": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "agentd",
			"version": "0.1.0",
		},
	})
}

func (s *Server) handleToolsList(w http.ResponseWriter, req jsonRPCRequest) {
	tools := []map[string]any{
		{
			"name":        "agentd_list_skills",
			"description": "List all loaded agentd skills",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			"name":        "agentd_trigger_skill",
			"description": "Manually trigger a skill by name",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{"type": "string", "description": "Skill name to trigger"},
				},
				"required": []string{"name"},
			},
		},
		{
			"name":        "agentd_get_history",
			"description": "Get recent events and actions",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "number", "description": "Number of items to return", "default": 10},
				},
			},
		},
		{
			"name":        "agentd_set_memory",
			"description": "Store a key-value pair in persistent memory",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key":   map[string]any{"type": "string"},
					"value": map[string]any{"type": "string"},
				},
				"required": []string{"key", "value"},
			},
		},
		{
			"name":        "agentd_get_memory",
			"description": "Retrieve a value from persistent memory",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"key": map[string]any{"type": "string"},
				},
				"required": []string{"key"},
			},
		},
		{
			"name":        "agentd_run",
			"description": "Run an arbitrary prompt against the agentd agent",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt": map[string]any{"type": "string", "description": "The prompt to send to the agent"},
				},
				"required": []string{"prompt"},
			},
		},
	}

	writeResult(w, req.ID, map[string]any{"tools": tools})
}

func (s *Server) handleToolsCall(w http.ResponseWriter, ctx context.Context, req jsonRPCRequest) {
	var params toolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		writeError(w, req.ID, -32602, "invalid params")
		return
	}

	var result any
	var err error

	switch params.Name {
	case "agentd_list_skills":
		result = s.listSkills()
	case "agentd_trigger_skill":
		name, _ := params.Arguments["name"].(string)
		result, err = s.triggerSkill(ctx, name)
	case "agentd_get_history":
		limit := 10
		if l, ok := params.Arguments["limit"].(float64); ok {
			limit = int(l)
		}
		result, err = s.getHistory(limit)
	case "agentd_set_memory":
		key, _ := params.Arguments["key"].(string)
		value, _ := params.Arguments["value"].(string)
		err = s.db.SetMemory(key, value)
		result = map[string]string{"status": "ok"}
	case "agentd_get_memory":
		key, _ := params.Arguments["key"].(string)
		var val string
		val, err = s.db.GetMemory(key)
		result = map[string]string{"key": key, "value": val}
	case "agentd_run":
		prompt, _ := params.Arguments["prompt"].(string)
		if s.agent != nil {
			var resp string
			resp, err = s.agent.RunPrompt(ctx, prompt)
			result = map[string]string{"response": resp}
		} else {
			err = fmt.Errorf("agent not available")
		}
	default:
		writeError(w, req.ID, -32601, "unknown tool: "+params.Name)
		return
	}

	if err != nil {
		writeResult(w, req.ID, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": fmt.Sprintf("Error: %s", err.Error())},
			},
			"isError": true,
		})
		return
	}

	text, _ := json.MarshalIndent(result, "", "  ")
	writeResult(w, req.ID, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(text)},
		},
	})
}

func (s *Server) listSkills() []map[string]string {
	var result []map[string]string
	for _, sk := range s.skills {
		result = append(result, map[string]string{
			"name":        sk.Manifest.Name,
			"description": sk.Manifest.Description,
			"path":        sk.Path,
		})
	}
	return result
}

func (s *Server) triggerSkill(ctx context.Context, name string) (string, error) {
	for _, sk := range s.skills {
		if sk.Manifest.Name == name {
			result, err := skills.Run(ctx, sk, map[string]any{"manual": true}, nil)
			if err != nil {
				return "", err
			}
			return result.Stdout, nil
		}
	}
	return "", fmt.Errorf("skill not found: %s", name)
}

func (s *Server) getHistory(limit int) (map[string]any, error) {
	events, err := s.db.RecentEvents(limit)
	if err != nil {
		return nil, err
	}
	actions, err := s.db.RecentActions(limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"events":  events,
		"actions": actions,
	}, nil
}

func writeResult(w http.ResponseWriter, id any, result any) {
	json.NewEncoder(w).Encode(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func writeError(w http.ResponseWriter, id any, code int, message string) {
	json.NewEncoder(w).Encode(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonRPCError{Code: code, Message: message},
	})
}
