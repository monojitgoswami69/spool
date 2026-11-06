// Package tunnel exposes a local port publicly for previews. It prefers
// cloudflared (trycloudflare.com) and falls back to localhost.run over SSH.
package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
)

// Options configures tunnel startup.
type Options struct {
	Binary         string // "cloudflared", "ssh", or "" to auto-detect
	SSHHost        string // defaults to nokey@localhost.run
	StartupTimeout time.Duration
}

// Tunnel is a running public tunnel.
type Tunnel struct {
	URL  string
	Mode string

	cmd   *exec.Cmd
	done  chan struct{}
	close bool
}

var publicURLRe = regexp.MustCompile(`https://[a-zA-Z0-9][a-zA-Z0-9.-]*\.(?:trycloudflare\.com|lhr\.life|localhost\.run)`)

// ExtractURL finds a supported public URL in a line of tunnel output.
func ExtractURL(line string) string {
	return publicURLRe.FindString(line)
}

// BuildCloudflaredArgs returns the argv for a quick cloudflared tunnel.
func BuildCloudflaredArgs(port int) []string {
	return []string{"tunnel", "--url", fmt.Sprintf("http://localhost:%d", port), "--no-autoupdate"}
}

// BuildSSHArgs returns the argv for a localhost.run reverse tunnel.
func BuildSSHArgs(port int, host string) []string {
	if host == "" {
		host = "nokey@localhost.run"
	}
	return []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=30",
		"-o", "ExitOnForwardFailure=yes",
		"-R", fmt.Sprintf("80:localhost:%d", port),
		host,
	}
}

// Start launches a tunnel and waits until a public URL is observed.
func Start(ctx context.Context, port int, opts Options) (*Tunnel, error) {
	if port < 1 || port > 65535 {
		return nil, domain.E(domain.KindValidation, "tunnel.Start", "invalid port", nil)
	}
	timeout := opts.StartupTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	mode := opts.Binary
	if mode == "" {
		if _, err := exec.LookPath("cloudflared"); err == nil {
			mode = "cloudflared"
		} else if _, err := exec.LookPath("ssh"); err == nil {
			mode = "ssh"
		} else {
			return nil, domain.E(domain.KindNotFound, "tunnel.Start", "neither cloudflared nor ssh is installed", nil)
		}
	}

	var cmd *exec.Cmd
	switch mode {
	case "cloudflared":
		if _, err := exec.LookPath("cloudflared"); err != nil {
			return nil, domain.E(domain.KindNotFound, "tunnel.Start", "cloudflared not found", err)
		}
		cmd = exec.CommandContext(ctx, "cloudflared", BuildCloudflaredArgs(port)...)
	case "ssh":
		if _, err := exec.LookPath("ssh"); err != nil {
			return nil, domain.E(domain.KindNotFound, "tunnel.Start", "ssh not found", err)
		}
		cmd = exec.CommandContext(ctx, "ssh", BuildSSHArgs(port, opts.SSHHost)...)
	default:
		return nil, domain.E(domain.KindValidation, "tunnel.Start", "unknown tunnel binary "+mode, nil)
	}

	r, w, err := os.Pipe()
	if err != nil {
		return nil, domain.E(domain.KindIO, "tunnel.Start", "pipe", err)
	}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		_ = r.Close()
		_ = w.Close()
		return nil, domain.E(domain.KindIO, "tunnel.Start", "start tunnel process", err)
	}
	_ = w.Close()

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	urlCh := make(chan string, 1)
	go func() {
		defer r.Close()
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			if u := ExtractURL(sc.Text()); u != "" {
				select {
				case urlCh <- u:
				default:
				}
			}
		}
	}()

	select {
	case u := <-urlCh:
		return &Tunnel{URL: u, Mode: mode, cmd: cmd, done: done}, nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return nil, domain.E(domain.KindHealth, "tunnel.Start",
			fmt.Sprintf("%s did not produce a public URL within %s", mode, timeout), nil)
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return nil, domain.E(domain.KindCancelled, "tunnel.Start", "context cancelled", ctx.Err())
	}
}

// PublicURL returns the assigned public URL.
func (t *Tunnel) PublicURL() string {
	if t == nil {
		return ""
	}
	return t.URL
}

// Close terminates the tunnel.
func (t *Tunnel) Close() error {
	if t == nil || t.close || t.cmd == nil || t.cmd.Process == nil {
		return nil
	}
	t.close = true
	_ = t.cmd.Process.Kill()
	select {
	case <-t.done:
	case <-time.After(5 * time.Second):
	}
	return nil
}

// ParsePort extracts a local port from a URL for diagnostics.
func ParsePort(u string) int {
	re := regexp.MustCompile(`:(\d{2,5})`)
	if m := re.FindStringSubmatch(u); m != nil {
		if p, err := strconv.Atoi(m[1]); err == nil {
			return p
		}
	}
	return 0
}

// Summary renders a one-line description.
func (t *Tunnel) Summary() string {
	if t == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprintf("%s via %s", t.URL, t.Mode))
}
