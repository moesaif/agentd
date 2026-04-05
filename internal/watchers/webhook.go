package watchers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/log"
)

type WebhookWatcher struct {
	port   int
	secret string
	server *http.Server
	done   chan struct{}
}

func NewWebhookWatcher(port int, secret string) *WebhookWatcher {
	return &WebhookWatcher{
		port:   port,
		secret: secret,
		done:   make(chan struct{}),
	}
}

func (ww *WebhookWatcher) Name() string { return "webhook" }

func (ww *WebhookWatcher) Start(events chan<- Event) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", ww.handleWebhook(events))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	ww.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", ww.port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("webhook listener started", "port", ww.port)
		if err := ww.server.ListenAndServe(); err != http.ErrServerClosed {
			log.Error("webhook server error", "error", err)
		}
	}()

	return nil
}

func (ww *WebhookWatcher) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return ww.server.Shutdown(ctx)
}

func (ww *WebhookWatcher) handleWebhook(events chan<- Event) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}

		// Verify signature if secret is configured
		if ww.secret != "" {
			sig := r.Header.Get("X-Hub-Signature-256")
			if sig == "" {
				sig = r.Header.Get("X-Gitlab-Token")
			}
			if !ww.verifySignature(body, sig) {
				http.Error(w, "invalid signature", http.StatusUnauthorized)
				return
			}
		}

		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			payload = map[string]any{"raw": string(body)}
		}

		eventType := detectEventType(r)

		events <- Event{
			Source:    "webhook",
			Type:      eventType,
			Payload:   payload,
			Timestamp: time.Now(),
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"accepted"}`)
	}
}

func (ww *WebhookWatcher) verifySignature(body []byte, signature string) bool {
	if signature == "" {
		return false
	}

	// GitHub signature format: sha256=<hex>
	if strings.HasPrefix(signature, "sha256=") {
		sig := strings.TrimPrefix(signature, "sha256=")
		mac := hmac.New(sha256.New, []byte(ww.secret))
		mac.Write(body)
		expected := hex.EncodeToString(mac.Sum(nil))
		return hmac.Equal([]byte(sig), []byte(expected))
	}

	// GitLab uses token comparison
	return signature == ww.secret
}

func detectEventType(r *http.Request) string {
	// GitHub
	if gh := r.Header.Get("X-GitHub-Event"); gh != "" {
		action := ""
		// Try to extract action from body for compound events
		var body map[string]any
		// We already read the body, so use the event header
		if action != "" {
			return "github." + gh + "." + action
		}
		_ = body
		return "github." + gh
	}

	// GitLab
	if gl := r.Header.Get("X-Gitlab-Event"); gl != "" {
		return "gitlab." + strings.ReplaceAll(strings.ToLower(gl), " ", "_")
	}

	// Generic
	return "webhook.generic"
}
