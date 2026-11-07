package web

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/monojitgoswami69/spool/internal/agent"
)

//go:embed assets/*
var assetsFS embed.FS

// Server is the HTTP control plane.
type Server struct {
	backend Backend
	store   *ChatStore
	mux     *http.ServeMux
	// Version is reported by /api/health. Callers should set it from the
	// build-injected cmd.Version instead of hardcoding a release string.
	Version string
}

// New wires routes and assets.
func New(backend Backend, store *ChatStore) *Server {
	s := &Server{backend: backend, store: store, mux: http.NewServeMux(), Version: "dev"}
	s.mux.HandleFunc("/api/projects", s.handleProjects)
	s.mux.HandleFunc("/api/config", s.handleConfig)
	s.mux.HandleFunc("/api/chats", s.handleChats)
	s.mux.HandleFunc("/api/chats/", s.handleChat)
	s.mux.HandleFunc("/api/chat", s.handleChatTurn)
	s.mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.Version})
	})
	sub, err := fs.Sub(assetsFS, "assets")
	if err == nil {
		fileServer := http.FileServer(http.FS(sub))
		s.mux.Handle("/assets/", http.StripPrefix("/assets/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			fileServer.ServeHTTP(w, r)
		})))
	}
	s.mux.HandleFunc("/", s.handleIndex)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; font-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := assetsFS.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, "index unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	projects, err := s.backend.Projects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	cfg, err := s.backend.Config(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *Server) handleChats(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		chats, err := s.store.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, chats)
	case http.MethodPost:
		var body struct {
			Project   string `json:"project"`
			ProjectID string `json:"project_id"`
		}
		_ = readJSON(r, &body)
		chat, err := s.store.Create(body.Project, body.ProjectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, chat)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/chats/")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		chat, err := s.store.Get(id)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, chat)
	case http.MethodDelete:
		if err := s.store.Delete(id); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(w)
	}
}

type sseEvent struct {
	Type              string        `json:"type"`
	Chat              *ChatSummary  `json:"chat,omitempty"`
	Event             *agent.Event  `json:"event,omitempty"`
	Message           *Message      `json:"message,omitempty"`
	NeedsConfirmation bool          `json:"needs_confirmation,omitempty"`
	Confirmation      *Confirmation `json:"confirmation,omitempty"`
	Error             string        `json:"error,omitempty"`
}

func (s *Server) handleChatTurn(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req ChatRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("message is empty"))
		return
	}

	var chat *Chat
	var err error
	if req.ChatID == "" {
		chat, err = s.store.Create(req.Project, "")
	} else {
		chat, err = s.store.Get(req.ChatID)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if req.Project == "" {
		req.Project = chat.Project
	}
	if !req.Retry {
		chat, err = s.store.Append(chat.ID, "user", req.Text, nil, nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func(ev sseEvent) bool {
		data, err := json.Marshal(ev)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	summary := summarize(chat)
	send(sseEvent{Type: "start", Chat: &summary})

	events := make([]agent.Event, 0, 16)
	var confirmation *Confirmation
	emit := func(ev agent.Event) {
		events = append(events, ev)
		if ev.Type == "permission_denied" {
			confirmation = &Confirmation{Tool: ev.Tool, Destructive: ev.Destructive, Args: ev.Args}
		}
		send(sseEvent{Type: "event", Event: &ev})
	}

	out, berr := s.backend.Send(r.Context(), req, emit)
	if out == nil {
		out = &Outcome{}
	}
	final := strings.TrimSpace(out.Final)
	if berr != nil && final == "" {
		final = "Error: " + berr.Error()
	}
	if final == "" {
		final = "No response."
	}
	if confirmation == nil && out.NeedsConfirmation && out.Confirmation != nil {
		confirmation = out.Confirmation
	}

	chat, err = s.store.Append(chat.ID, "assistant", final, out.Intent, events)
	if err != nil {
		send(sseEvent{Type: "error", Error: err.Error()})
		return
	}
	if out.Project != "" && (out.Project != chat.Project || out.ProjectID != chat.ProjectID) {
		if updated, uerr := s.store.UpdateProject(chat.ID, out.Project, out.ProjectID); uerr == nil {
			chat = updated
		}
	}
	chatSummary := summarize(chat)
	var last *Message
	if len(chat.Messages) > 0 {
		m := chat.Messages[len(chat.Messages)-1]
		last = &m
	}
	send(sseEvent{
		Type: "done", Chat: &chatSummary, Message: last,
		NeedsConfirmation: confirmation != nil, Confirmation: confirmation,
	})
}

func summarize(c *Chat) ChatSummary {
	return ChatSummary{
		ID: c.ID, Title: c.Title, Project: c.Project, ProjectID: c.ProjectID,
		UpdatedAt: c.UpdatedAt, MessageCount: len(c.Messages),
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, POST, DELETE")
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func readJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	return dec.Decode(dst)
}
