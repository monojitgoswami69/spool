// Package portprobe probes host TCP sockets and allocates conflict-free host
// ports for both application services and inferred infrastructure containers.
// The allocator is concurrency-safe and reserves every granted port for the
// lifetime of the allocator so two services can never be handed the same port.
package portprobe

import (
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
)

const (
	// DefaultHost is the loopback interface probed by default.
	DefaultHost = "0.0.0.0"
	// MinPort is the lowest unprivileged port Spool will publish.
	MinPort = 1024
	// MaxPort is the highest valid TCP port.
	MaxPort = 65535
	// DefaultAppPort is the fallback preferred port.
	DefaultAppPort = 3000
	// DefaultMaxScan bounds how many candidates a single allocation tries.
	DefaultMaxScan = 2000
)

// Options configures an Allocator.
type Options struct {
	Host    string
	MaxScan int
}

// Allocator hands out unique, currently-free host ports.
type Allocator struct {
	mu      sync.Mutex
	host    string
	maxScan int
	used    map[int]struct{}
}

// New returns an allocator probing loopback.
func New() *Allocator { return NewWithOptions(Options{}) }

// NewWithOptions returns an allocator with explicit options.
func NewWithOptions(o Options) *Allocator {
	host := o.Host
	if host == "" {
		host = DefaultHost
	}
	scan := o.MaxScan
	if scan <= 0 {
		scan = DefaultMaxScan
	}
	return &Allocator{host: host, maxScan: scan, used: make(map[int]struct{})}
}

// Reserve marks ports as used without probing them.
func (a *Allocator) Reserve(ports ...int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, p := range ports {
		if p > 0 && p <= MaxPort {
			a.used[p] = struct{}{}
		}
	}
}

// IsReserved reports whether a port has already been granted or reserved.
func (a *Allocator) IsReserved(port int) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.used[port]
	return ok
}

// Allocate returns a free host port, preferring preferred. If preferred is
// outside the unprivileged range it is replaced by DefaultAppPort. The returned
// port is reserved atomically so concurrent callers never collide.
func (a *Allocator) Allocate(preferred int) (int, error) {
	if preferred < MinPort || preferred > MaxPort {
		preferred = DefaultAppPort
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	port := preferred
	for i := 0; i < a.maxScan; i++ {
		if port > MaxPort {
			port = MinPort + (port - MaxPort - 1)
		}
		if _, taken := a.used[port]; !taken && IsFreeOn(a.host, port) {
			a.used[port] = struct{}{}
			return port, nil
		}
		port++
	}
	return 0, domain.E(domain.KindConfig, "portprobe.Allocate",
		fmt.Sprintf("no free port near %d after %d attempts", preferred, a.maxScan), domain.ErrPortUnavailable)
}

// AllocateMany allocates a free port for each preferred value, in order.
func (a *Allocator) AllocateMany(preferred ...int) ([]int, error) {
	out := make([]int, 0, len(preferred))
	for _, p := range preferred {
		got, err := a.Allocate(p)
		if err != nil {
			return out, err
		}
		out = append(out, got)
	}
	return out, nil
}

// IsFree reports whether port is free on the default host.
func IsFree(port int) bool { return IsFreeOn(DefaultHost, port) }

// IsFreeOn reports whether host:port can be bound to right now. There is an
// unavoidable TOCTOU window between this probe and the real bind; callers
// should use Allocator to also serialize Spool's own allocations.
func IsFreeOn(host string, port int) bool {
	if port < 1 || port > MaxPort {
		return false
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// ProbeResult describes a TCP probe.
type ProbeResult struct {
	Host string
	Port int
	Open bool
	Err  string
}

// Probe attempts to dial host:port within the caller's context deadline.
func Probe(host string, port int, timeoutMillis int) ProbeResult {
	if timeoutMillis <= 0 {
		timeoutMillis = 300
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, time.Duration(timeoutMillis)*time.Millisecond)
	if err != nil {
		return ProbeResult{Host: host, Port: port, Open: false, Err: err.Error()}
	}
	_ = conn.Close()
	return ProbeResult{Host: host, Port: port, Open: true}
}
