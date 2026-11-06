// Package httpcheck performs real HTTP readiness probes against live endpoints.
// Any response below 500 (including 401/404/3xx) counts as healthy, proving the
// process is serving; 5xx is an application error.
package httpcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
)

// Status is the probe outcome.
type Status string

const (
	Healthy     Status = "healthy"
	AppError    Status = "application_error"
	Unreachable Status = "unreachable"
	Timeout     Status = "timeout"
)

// DefaultTimeout is the per-probe timeout.
const DefaultTimeout = 5 * time.Second

var client = &http.Client{
	// Do not follow redirects: a 3xx proves the app answered.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Timeout: DefaultTimeout,
}

// Check probes url.
func Check(ctx context.Context, url string, timeout time.Duration) domain.Result {
	res := domain.Result{URL: url}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		res.Status = string(Unreachable)
		res.Err = err.Error()
		return res
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(cctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
			res.Status = string(Timeout)
		} else {
			res.Status = string(Unreachable)
		}
		res.Err = err.Error()
		return res
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body) // drain for connection reuse
	res.Code = resp.StatusCode
	if resp.StatusCode >= 500 {
		res.Status = string(AppError)
	} else {
		res.Status = string(Healthy)
	}
	return res
}

// Services probes every non-worker service with a host port.
func Services(ctx context.Context, svcs []*domain.Service, portMap map[string]int) []domain.Result {
	var results []domain.Result
	for _, svc := range svcs {
		if svc.IsWorker {
			continue
		}
		hostPort := portMap[svc.Name]
		if hostPort == 0 {
			hostPort = svc.HostPort
		}
		if hostPort == 0 {
			continue
		}
		r := Check(ctx, fmt.Sprintf("http://localhost:%d/", hostPort), DefaultTimeout)
		r.Service = svc.Name
		results = append(results, r)
	}
	return results
}

// AllHealthy reports whether every result is healthy. An empty set is healthy.
func AllHealthy(results []domain.Result) bool {
	for _, r := range results {
		if !r.Healthy() {
			return false
		}
	}
	return true
}
