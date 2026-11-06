package domain

import "context"

// LLMClient is the minimal text-completion seam used by the healer and agent.
// Implementations must be safe for concurrent use.
type LLMClient interface {
	Call(ctx context.Context, systemPrompt, userPrompt string) (string, error)
	Provider() string
	Model() string
}

// ComposeController is the container-orchestration seam. The docker package is
// the production implementation; the agent and healer depend only on this.
type ComposeController interface {
	Build(ctx context.Context, service string) (string, error)
	Up(ctx context.Context, extra ...string) (string, error)
	Down(ctx context.Context, volumes bool) (string, error)
	Ps(ctx context.Context) ([]Container, error)
	Logs(ctx context.Context, service string, tail int) (string, error)
	Stats(ctx context.Context) ([]ContainerStat, error)
	Restart(ctx context.Context, service string) (string, error)
	Stop(ctx context.Context, service string) (string, error)
	Start(ctx context.Context, service string) (string, error)
	Recreate(ctx context.Context, service string) (string, error)
	Available() bool
}

// HTTPProber is the readiness probe seam.
type HTTPProber interface {
	Check(ctx context.Context, url string) Result
}

// Result is the outcome of an HTTP readiness probe.
type Result struct {
	Service string `json:"service"`
	URL     string `json:"url"`
	Status  string `json:"status"` // healthy | application_error | unreachable | timeout
	Code    int    `json:"code"`
	Err     string `json:"err,omitempty"`
}

// Healthy reports whether the endpoint responded below 500.
func (r Result) Healthy() bool { return r.Status == "healthy" }
