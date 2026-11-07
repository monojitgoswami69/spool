// Package web implements Spool's local web control plane: a minimal HTTP
// server, persistent chat store, and an embedded single-page agent console.
package web

import (
	"context"
	"time"

	"github.com/monojitgoswami69/spool/internal/agent"
)

// ProjectView is the sidebar/project summary.
type ProjectView struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Services int       `json:"services"`
	Infra    int       `json:"infra"`
	Running  int       `json:"running"`
	Total    int       `json:"total"`
	Healthy  bool      `json:"healthy"`
	URLs     []string  `json:"urls,omitempty"`
	LastInit time.Time `json:"last_init"`
}

// ConfigView is redacted global configuration.
type ConfigView struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Source        string `json:"source"`
	KeyConfigured bool   `json:"key_configured"`
	Home          string `json:"home"`
}

// ChatRequest is a single agent turn.
type ChatRequest struct {
	ChatID  string `json:"chat_id"`
	Project string `json:"project"`
	Text    string `json:"text"`
	Confirm bool   `json:"confirm"`
	Retry   bool   `json:"retry"`
}

// Confirmation describes a pending mutating action.
type Confirmation struct {
	Tool        string         `json:"tool"`
	Destructive bool           `json:"destructive"`
	Args        map[string]any `json:"args,omitempty"`
}

// Outcome is the result of one agent turn.
type Outcome struct {
	Final             string        `json:"final"`
	Intent            *agent.Intent `json:"intent,omitempty"`
	Project           string        `json:"project,omitempty"`
	ProjectID         string        `json:"project_id,omitempty"`
	NeedsConfirmation bool          `json:"needs_confirmation,omitempty"`
	Confirmation      *Confirmation `json:"confirmation,omitempty"`
	Events            []agent.Event `json:"events,omitempty"`
}

// Backend is implemented by the CLI layer and drives the agent.
type Backend interface {
	Projects(ctx context.Context) ([]ProjectView, error)
	Config(ctx context.Context) (ConfigView, error)
	Send(ctx context.Context, req ChatRequest, emit func(agent.Event)) (*Outcome, error)
}

// Message is one stored chat message.
type Message struct {
	ID        string        `json:"id"`
	Role      string        `json:"role"` // user | assistant
	Text      string        `json:"text"`
	Intent    *agent.Intent `json:"intent,omitempty"`
	Events    []agent.Event `json:"events,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
}

// Chat is a persisted conversation.
type Chat struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Project   string    `json:"project,omitempty"`
	ProjectID string    `json:"project_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Messages  []Message `json:"messages"`
}

// ChatSummary is the sidebar representation.
type ChatSummary struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Project      string    `json:"project,omitempty"`
	ProjectID    string    `json:"project_id,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
	MessageCount int       `json:"message_count"`
}
