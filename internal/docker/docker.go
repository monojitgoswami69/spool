// Package docker is Spool's single boundary for invoking docker/compose. All
// container operations go through it; nothing else shells out to Docker.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/monojitgoswami69/spool/internal/domain"
)

// Compose wraps a docker compose project.
type Compose struct {
	File    string
	Dir     string
	Project string

	once sync.Once
	v1   bool
}

// New constructs a compose handle. project is the compose project name.
func New(file, dir, project string) *Compose {
	return &Compose{File: file, Dir: dir, Project: project}
}

// Available reports whether a docker binary is installed.
func Available() bool {
	if _, err := exec.LookPath("docker"); err == nil {
		return true
	}
	if _, err := exec.LookPath("docker-compose"); err == nil {
		return true
	}
	return false
}

// Available implements domain.ComposeController.
func (c *Compose) Available() bool { return Available() }

// DaemonRunning verifies the Docker daemon answers.
func DaemonRunning(ctx context.Context) bool {
	out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

func (c *Compose) detect() {
	c.once.Do(func() {
		if _, err := exec.LookPath("docker"); err == nil {
			// Verify the compose v2 plugin, otherwise prefer docker-compose.
			if err := exec.Command("docker", "compose", "version").Run(); err == nil {
				c.v1 = false
				return
			}
		}
		c.v1 = true
	})
}

func (c *Compose) command(ctx context.Context, args ...string) (*exec.Cmd, string) {
	c.detect()
	var bin string
	var full []string
	if c.v1 {
		bin = "docker-compose"
		full = []string{"-f", c.File}
	} else {
		bin = "docker"
		full = []string{"compose", "-f", c.File}
	}
	if c.Project != "" {
		full = append(full, "-p", c.Project)
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, bin, full...)
	cmd.Dir = c.Dir
	return cmd, bin
}

func (c *Compose) run(ctx context.Context, op string, args ...string) (string, error) {
	cmd, _ := c.command(ctx, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil {
		return out, domain.E(domain.KindDocker, "docker."+op, fmt.Sprintf("%s failed: %s", op, TailLines(out, 20)), err)
	}
	return out, nil
}

// Build builds all services (service=="" ) or one service.
func (c *Compose) Build(ctx context.Context, service string) (string, error) {
	args := []string{"build"}
	if service != "" {
		args = append(args, service)
	}
	return c.run(ctx, "Build", args...)
}

// Up starts the stack in the background.
func (c *Compose) Up(ctx context.Context, extra ...string) (string, error) {
	args := append([]string{"up", "-d"}, extra...)
	return c.run(ctx, "Up", args...)
}

// Down stops the stack, optionally removing volumes.
func (c *Compose) Down(ctx context.Context, volumes bool) (string, error) {
	args := []string{"down"}
	if volumes {
		args = append(args, "-v")
	}
	return c.run(ctx, "Down", args...)
}

// Restart restarts a service (or all when service is empty).
func (c *Compose) Restart(ctx context.Context, service string) (string, error) {
	args := []string{"restart"}
	if service != "" {
		args = append(args, service)
	}
	return c.run(ctx, "Restart", args...)
}

// Stop stops a service.
func (c *Compose) Stop(ctx context.Context, service string) (string, error) {
	return c.run(ctx, "Stop", "stop", service)
}

// Start starts a stopped service.
func (c *Compose) Start(ctx context.Context, service string) (string, error) {
	return c.run(ctx, "Start", "start", service)
}

// Recreate force-recreates a single service without touching dependencies.
func (c *Compose) Recreate(ctx context.Context, service string) (string, error) {
	return c.run(ctx, "Recreate", "up", "-d", "--force-recreate", "--no-deps", service)
}

// Logs returns the last tail lines for a service.
func (c *Compose) Logs(ctx context.Context, service string, tail int) (string, error) {
	if tail <= 0 {
		tail = 200
	}
	args := []string{"logs", "--no-color", "--tail", strconv.Itoa(tail)}
	if service != "" {
		args = append(args, service)
	}
	return c.run(ctx, "Logs", args...)
}

// Follow streams logs to w until the context is cancelled.
func (c *Compose) Follow(ctx context.Context, service string, tail int, w io.Writer) error {
	if tail <= 0 {
		tail = 200
	}
	args := []string{"logs", "-f", "--no-color", "--tail", strconv.Itoa(tail)}
	if service != "" {
		args = append(args, service)
	}
	cmd, _ := c.command(ctx, args...)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return domain.E(domain.KindDocker, "docker.Follow", "log follow failed", err)
	}
	return nil
}

// StreamLogs returns a reader of followed logs plus a cleanup function.
func (c *Compose) StreamLogs(ctx context.Context, service string) (io.ReadCloser, func(), error) {
	args := []string{"logs", "-f", "--no-color", "--tail", "200"}
	if service != "" {
		args = append(args, service)
	}
	cmd, _ := c.command(ctx, args...)
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, nil, domain.E(domain.KindDocker, "docker.StreamLogs", "start", err)
	}
	cleanup := func() {
		_ = w.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		_ = r.Close()
	}
	return r, cleanup, nil
}

// Validate renders the compose file with the Docker CLI to catch schema errors
// before any build or up.
func (c *Compose) Validate(ctx context.Context) error {
	_, err := c.run(ctx, "Validate", "config", "--quiet")
	return err
}

// Ps returns parsed container rows.
func (c *Compose) Ps(ctx context.Context) ([]domain.Container, error) {
	out, err := c.run(ctx, "Ps", "ps", "--format", "json", "--all")
	if err != nil {
		return nil, err
	}
	return ParsePs(out)
}

// Stats returns docker stats rows.
func (c *Compose) Stats(ctx context.Context) ([]domain.ContainerStat, error) {
	cmd := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{json .}}")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		return nil, domain.E(domain.KindDocker, "docker.Stats", "stats failed", err)
	}
	return ParseStats(buf.String())
}

// ParsePs parses compose ps output in either NDJSON or JSON-array form.
func ParsePs(out string) ([]domain.Container, error) {
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var rows []psRow
		if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
			return nil, domain.E(domain.KindDocker, "docker.ParsePs", "invalid JSON array", err)
		}
		return convertRows(rows), nil
	}
	var containers []domain.Container
	sc := bufio.NewScanner(strings.NewReader(trimmed))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row psRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		containers = append(containers, row.toContainer())
	}
	return containers, nil
}

type psRow struct {
	ID       string `json:"ID"`
	Name     string `json:"Name"`
	Service  string `json:"Service"`
	State    string `json:"State"`
	Status   string `json:"Status"`
	Health   string `json:"Health"`
	Image    string `json:"Image"`
	Project  string `json:"Project"`
	ExitCode int    `json:"ExitCode"`
}

func (r psRow) toContainer() domain.Container {
	health := strings.ToLower(strings.TrimSpace(r.Health))
	if health == "" {
		status := strings.ToLower(r.Status)
		switch {
		case strings.Contains(status, "(healthy)"):
			health = "healthy"
		case strings.Contains(status, "(unhealthy)"):
			health = "unhealthy"
		case strings.Contains(status, "health: starting"), strings.Contains(status, "(health: starting)"):
			health = "starting"
		}
	}
	return domain.Container{
		ID: r.ID, Name: r.Name, Service: r.Service, State: r.State,
		Status: r.Status, Health: health, Image: r.Image, Project: r.Project, ExitCode: r.ExitCode,
	}
}

func convertRows(rows []psRow) []domain.Container {
	out := make([]domain.Container, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toContainer())
	}
	return out
}

// ParseStats parses `docker stats --format {{json .}}` output (NDJSON).
func ParseStats(out string) ([]domain.ContainerStat, error) {
	var stats []domain.ContainerStat
	sc := bufio.NewScanner(strings.NewReader(strings.TrimSpace(out)))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var raw struct {
			Container string `json:"Container"`
			Name      string `json:"Name"`
			CPUPerc   string `json:"CPUPerc"`
			MemPerc   string `json:"MemPerc"`
			MemUsage  string `json:"MemUsage"`
			NetIO     string `json:"NetIO"`
			BlockIO   string `json:"BlockIO"`
			PIDs      string `json:"PIDs"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		stats = append(stats, domain.ContainerStat{
			Container: raw.Container,
			Name:      raw.Name,
			CPUPerc:   parsePercent(raw.CPUPerc),
			MemPerc:   parsePercent(raw.MemPerc),
			MemUsage:  raw.MemUsage,
			NetIO:     raw.NetIO,
			BlockIO:   raw.BlockIO,
			PIDs:      raw.PIDs,
		})
	}
	return stats, nil
}

func parsePercent(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

var (
	failedQuoteRe = regexp.MustCompile(`(?i)service\s+['"]([a-z0-9_.-]+)['"]\s+failed`)
	failedBuildRe = regexp.MustCompile(`(?i)failed to (?:build|solve)\s+([a-z0-9_.-]+)`)
	stepRe        = regexp.MustCompile(`\[([a-z0-9][a-z0-9_-]*)(?:\s+[a-z0-9_.-]+)?\s+\d+/\d+\]`)
	dockerfileRe  = regexp.MustCompile(`Dockerfile\.([a-z0-9][a-z0-9_-]*)`)
)

// ExtractFailedService best-effort identifies the service that failed a build.
func ExtractFailedService(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if m := failedQuoteRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
		if m := failedBuildRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
		if m := dockerfileRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
		if isErrorLine(line) {
			for j := i; j >= 0 && j > i-20; j-- {
				if m := stepRe.FindStringSubmatch(lines[j]); m != nil {
					return m[1]
				}
			}
		}
	}
	return ""
}

func isErrorLine(line string) bool {
	l := strings.ToLower(line)
	return strings.Contains(l, "error") || strings.Contains(l, "failed")
}

// TailLines returns the last n lines of s.
func TailLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s + "\n"
	}
	return strings.Join(lines[len(lines)-n:], "\n") + "\n"
}
