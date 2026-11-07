package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/monojitgoswami69/spool/internal/agent"
)

type fakeBackend struct {
	emitConfirm bool
}

func (f *fakeBackend) Projects(context.Context) ([]ProjectView, error) {
	return []ProjectView{{ID: "demo-1", Name: "demo", Services: 1, Infra: 0, Running: 1, Total: 1, Healthy: true, URLs: []string{"http://localhost:3000"}}}, nil
}
func (f *fakeBackend) Config(context.Context) (ConfigView, error) {
	return ConfigView{Provider: "gemini", Model: "gemini-3.5-flash-lite", Source: "test", KeyConfigured: true}, nil
}
func (f *fakeBackend) Send(ctx context.Context, req ChatRequest, emit func(agent.Event)) (*Outcome, error) {
	emit(agent.Event{Type: "intent", Message: "status", Args: map[string]any{"kind": "status", "project": "demo", "confidence": 0.95}})
	emit(agent.Event{Type: "tool_start", Tool: "inspect_logs_and_health", Args: map[string]any{"service": "web"}})
	emit(agent.Event{Type: "tool_end", Tool: "inspect_logs_and_health", Success: true, Message: "ok", Duration: 5 * time.Millisecond, Bytes: 12})
	if f.emitConfirm {
		emit(agent.Event{Type: "permission_denied", Tool: "orchestrate_stack", Args: map[string]any{"action": "nuke"}, Destructive: true})
		return &Outcome{Final: "Onboarding was not approved.", NeedsConfirmation: true, Confirmation: &Confirmation{Tool: "orchestrate_stack", Destructive: true, Args: map[string]any{"action": "nuke"}}}, nil
	}
	emit(agent.Event{Type: "final", Message: "**demo** is healthy"})
	return &Outcome{Final: "**demo** is healthy", Intent: &agent.Intent{Kind: agent.IntentStatus, Project: "demo"}, Project: "demo", ProjectID: "demo-1"}, nil
}

func newTestServer(t *testing.T, backend Backend) (*httptest.Server, *ChatStore) {
	t.Helper()
	store, err := NewChatStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(New(backend, store)), store
}

func TestChatStoreLifecycle(t *testing.T) {
	store, err := NewChatStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chat, err := store.Create("demo", "demo-1")
	if err != nil {
		t.Fatal(err)
	}
	chat, err = store.Append(chat.ID, "user", "what is running?", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if chat.Title != "what is running?" {
		t.Fatalf("title = %q", chat.Title)
	}
	chat, err = store.Append(chat.ID, "assistant", "healthy", &agent.Intent{Kind: agent.IntentStatus}, []agent.Event{{Type: "final", Message: "healthy"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(chat.Messages) != 2 {
		t.Fatalf("messages = %d", len(chat.Messages))
	}
	chat, err = store.UpdateProject(chat.ID, "other", "other-1")
	if err != nil || chat.Project != "other" {
		t.Fatalf("update project: %+v %v", chat, err)
	}
	list, err := store.List()
	if err != nil || len(list) != 1 || list[0].MessageCount != 2 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if _, err := store.Get("../escape"); err == nil {
		t.Fatal("expected invalid id rejection")
	}
	if err := store.Delete(chat.ID); err != nil {
		t.Fatal(err)
	}
	list, _ = store.List()
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %+v", list)
	}
}

func TestServerProjectsConfigAndAssets(t *testing.T) {
	srv, _ := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/projects")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var projects []ProjectView
	if err := json.NewDecoder(res.Body).Decode(&projects); err != nil || len(projects) != 1 {
		t.Fatalf("projects = %+v %v", projects, err)
	}

	res, err = http.Get(srv.URL + "/api/config")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var cfg ConfigView
	if err := json.NewDecoder(res.Body).Decode(&cfg); err != nil || cfg.Provider != "gemini" {
		t.Fatalf("config = %+v %v", cfg, err)
	}

	res, err = http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(res.Body)
	if !strings.Contains(buf.String(), `id="app"`) || !strings.Contains(buf.String(), "Spool") {
		t.Fatalf("index missing app shell")
	}
}

func TestServerChatTurnAndPersistence(t *testing.T) {
	srv, store := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	chat, err := store.Create("demo", "demo-1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(ChatRequest{ChatID: chat.ID, Text: "what is running?"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	scanner := bufio.NewScanner(res.Body)
	var sawDone bool
	var done sseEvent
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("sse json: %v", err)
		}
		if ev.Type == "done" {
			sawDone = true
			done = ev
		}
	}
	if !sawDone {
		t.Fatal("no done event")
	}
	if done.Message == nil || !strings.Contains(done.Message.Text, "healthy") {
		t.Fatalf("done message = %+v", done.Message)
	}
	if done.Chat == nil || done.Chat.Project != "demo" {
		t.Fatalf("done chat = %+v", done.Chat)
	}
	loaded, err := store.Get(chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("stored messages = %d", len(loaded.Messages))
	}
	if loaded.Messages[1].Intent == nil || loaded.Messages[1].Intent.Kind != agent.IntentStatus {
		t.Fatalf("assistant intent not stored: %+v", loaded.Messages[1])
	}
}

func TestServerConfirmationStream(t *testing.T) {
	srv, store := newTestServer(t, &fakeBackend{emitConfirm: true})
	defer srv.Close()
	chat, _ := store.Create("demo", "demo-1")
	body, _ := json.Marshal(ChatRequest{ChatID: chat.ID, Text: "nuke demo"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/chat", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var done sseEvent
	scanner := bufio.NewScanner(res.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev sseEvent
		_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev)
		if ev.Type == "done" {
			done = ev
		}
	}
	if !done.NeedsConfirmation || done.Confirmation == nil || done.Confirmation.Tool != "orchestrate_stack" {
		t.Fatalf("expected confirmation, got %+v", done)
	}
	if !done.Confirmation.Destructive {
		t.Fatal("expected destructive confirmation")
	}
}

func TestServerChatsAPI(t *testing.T) {
	srv, _ := newTestServer(t, &fakeBackend{})
	defer srv.Close()

	res, err := http.Post(srv.URL+"/api/chats", "application/json", strings.NewReader(`{"project":"demo","project_id":"demo-1"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var chat Chat
	if err := json.NewDecoder(res.Body).Decode(&chat); err != nil {
		t.Fatal(err)
	}
	if chat.ID == "" || chat.Project != "demo" {
		t.Fatalf("chat = %+v", chat)
	}

	res, err = http.Get(srv.URL + "/api/chats")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var chats []ChatSummary
	if err := json.NewDecoder(res.Body).Decode(&chats); err != nil || len(chats) != 1 {
		t.Fatalf("chats = %+v %v", chats, err)
	}

	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/chats/"+chat.ID, nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", res.StatusCode)
	}
}

func TestAssetsContainModalAndRetryFixes(t *testing.T) {
	srv, _ := newTestServer(t, &fakeBackend{})
	defer srv.Close()
	res, err := http.Get(srv.URL + "/assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var css bytes.Buffer
	_, _ = css.ReadFrom(res.Body)
	for _, want := range []string{"[hidden]", "#faf7f0", "data-theme=\"dark\""} {
		if !strings.Contains(css.String(), want) {
			t.Errorf("CSS missing %q", want)
		}
	}
	res, err = http.Get(srv.URL + "/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var js bytes.Buffer
	_, _ = js.ReadFrom(res.Body)
	for _, want := range []string{"state.queued", "confirmPending", "[hidden]"} {
		if want == "[hidden]" {
			continue
		}
		if !strings.Contains(js.String(), want) {
			t.Errorf("JS missing %q", want)
		}
	}
}
