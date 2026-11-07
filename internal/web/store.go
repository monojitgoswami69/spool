package web

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/monojitgoswami69/spool/internal/agent"
	"github.com/monojitgoswami69/spool/internal/safefs"
)

// ChatStore persists conversations under a directory as 0600 JSON.
type ChatStore struct {
	dir string
	mu  sync.Mutex
}

// NewChatStore creates the store directory if needed.
func NewChatStore(dir string) (*ChatStore, error) {
	if err := safefs.EnsureDir(dir, 0o700); err != nil {
		return nil, err
	}
	return &ChatStore{dir: dir}, nil
}

func newID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func validChatID(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func (s *ChatStore) path(id string) (string, error) {
	if !validChatID(id) {
		return "", fmt.Errorf("invalid chat id")
	}
	return filepath.Join(s.dir, id+".json"), nil
}

func (s *ChatStore) load(id string) (*Chat, error) {
	p, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var c Chat
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *ChatStore) saveLocked(c *Chat) error {
	p, err := s.path(c.ID)
	if err != nil {
		return err
	}
	c.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return safefs.WriteFileAtomic(p, append(data, '\n'), 0o600)
}

// Create starts a new conversation.
func (s *ChatStore) Create(project, projectID string) (*Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	c := &Chat{
		ID: newID(8), Title: "New chat", Project: project, ProjectID: projectID,
		CreatedAt: now, UpdatedAt: now, Messages: []Message{},
	}
	if err := s.saveLocked(c); err != nil {
		return nil, err
	}
	return c, nil
}

// List returns conversations ordered by recency.
func (s *ChatStore) List() ([]ChatSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []ChatSummary
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		c, err := s.load(id)
		if err != nil {
			continue
		}
		out = append(out, ChatSummary{
			ID: c.ID, Title: c.Title, Project: c.Project, ProjectID: c.ProjectID,
			UpdatedAt: c.UpdatedAt, MessageCount: len(c.Messages),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// Get loads a conversation.
func (s *ChatStore) Get(id string) (*Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(id)
}

// Delete removes a conversation.
func (s *ChatStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Append adds a message and returns the updated chat.
func (s *ChatStore) Append(id, role, text string, intent *agent.Intent, events []agent.Event) (*Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.load(id)
	if err != nil {
		return nil, err
	}
	msg := Message{
		ID: newID(6), Role: role, Text: text, Intent: intent, Events: events,
		CreatedAt: time.Now().UTC(),
	}
	c.Messages = append(c.Messages, msg)
	if role == "user" && (c.Title == "" || c.Title == "New chat") {
		c.Title = chatTitle(text)
	}
	if err := s.saveLocked(c); err != nil {
		return nil, err
	}
	return c, nil
}

// UpdateProject rebinds a chat to a project.
func (s *ChatStore) UpdateProject(id, project, projectID string) (*Chat, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.load(id)
	if err != nil {
		return nil, err
	}
	c.Project, c.ProjectID = project, projectID
	if err := s.saveLocked(c); err != nil {
		return nil, err
	}
	return c, nil
}

func chatTitle(text string) string {
	t := strings.TrimSpace(text)
	t = strings.ReplaceAll(t, "\n", " ")
	if strings.HasPrefix(t, "/") {
		// Keep the command visible, e.g. "/onboard owner/repo".
		t = strings.TrimSpace(t)
	}
	r := []rune(t)
	if len(r) > 64 {
		t = string(r[:64]) + "…"
	}
	if t == "" {
		t = "New chat"
	}
	return t
}
