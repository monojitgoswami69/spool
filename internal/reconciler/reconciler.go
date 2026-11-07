// Package reconciler assigns conflict-free host ports to application services,
// rewrites framework start commands to bind on 0.0.0.0, and wires inter-service
// URLs. Infrastructure host ports are allocated before this runs so apps can
// never collide with them.
package reconciler

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/portprobe"
)

// Reconcile mutates services with host ports, bind env, and start commands.
func Reconcile(services []*domain.Service, alloc *portprobe.Allocator, globalEnv map[string]string) error {
	return ReconcileWithExisting(services, alloc, globalEnv, nil)
}

// ReconcileWithExisting behaves like Reconcile but preserves pre-existing host
// port assignments (used by `spool update` so a running stack keeps its URLs).
func ReconcileWithExisting(services []*domain.Service, alloc *portprobe.Allocator, globalEnv map[string]string, existing map[string]int) error {
	if alloc == nil {
		alloc = portprobe.New()
	}
	globalPort := 0
	if p, ok := globalEnv["PORT"]; ok {
		if n, err := strconv.Atoi(strings.TrimSpace(p)); err == nil && n > 0 {
			globalPort = n
		}
	}

	for _, svc := range services {
		if svc.Env == nil {
			svc.Env = map[string]string{}
		}
		if svc.IsWorker {
			svc.Port = 0
			svc.HostPort = 0
			continue
		}
		target := svc.DefaultPort
		if target <= 0 {
			target = 3000
		}
		if v := strings.TrimSpace(svc.Env["PORT"]); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				target = n
			}
		} else if len(services) == 1 && globalPort > 0 {
			target = globalPort
		}
		hostPort := 0
		if existing != nil {
			if kept, ok := existing[svc.Name]; ok && kept > 0 {
				hostPort = kept
				alloc.Reserve(kept)
			}
		}
		if hostPort == 0 {
			var err error
			hostPort, err = alloc.Allocate(target)
			if err != nil {
				return fmt.Errorf("reconciler: allocate host port for %s: %w", svc.Name, err)
			}
		}
		svc.Port = target
		svc.HostPort = hostPort
		svc.StartCmd = RewriteStartCommand(svc.StartCmd, svc.Framework, target)
		svc.Env["PORT"] = strconv.Itoa(target)
		svc.Env["HOST"] = "0.0.0.0"
	}

	for _, svc := range services {
		if svc.IsWorker || svc.Port <= 0 {
			continue
		}
		internalURL := fmt.Sprintf("http://%s:%d", svc.Name, svc.Port)
		upper := strings.ToUpper(strings.ReplaceAll(svc.Name, "-", "_"))
		base := strings.TrimSuffix(upper, "_SERVICE")
		for _, key := range []string{upper + "_URL", base + "_URL", "LOCAL_" + base + "_URL"} {
			if v, ok := (svc.Env)[key]; !ok || v == "" || strings.Contains(v, "localhost") || strings.Contains(v, "127.0.0.1") {
				svc.Env[key] = internalURL
			}
		}
		if isBackend(svc) {
			publicURL := fmt.Sprintf("http://localhost:%d", svc.HostPort)
			for _, key := range []string{"NEXT_PUBLIC_API_URL", "VITE_API_URL", "REACT_APP_API_URL", "PUBLIC_API_URL"} {
				if v, ok := svc.Env[key]; !ok || v == "" || strings.Contains(v, "your_") || strings.Contains(v, "<") {
					svc.Env[key] = publicURL
				}
			}
			for _, key := range []string{"API_URL", "BACKEND_URL", "SERVER_URL"} {
				if v, ok := svc.Env[key]; !ok || v == "" || strings.Contains(v, "localhost") || strings.Contains(v, "127.0.0.1") {
					svc.Env[key] = internalURL
				}
			}
		}
	}

	// Point frontends at the first backend so localhost/placeholder URLs never
	// leak into the container network.
	var backends []*domain.Service
	for _, svc := range services {
		if !svc.IsWorker && svc.Port > 0 && isBackend(svc) {
			backends = append(backends, svc)
		}
	}
	if len(backends) > 0 {
		be := backends[0]
		publicURL := fmt.Sprintf("http://localhost:%d", be.HostPort)
		internalURL := fmt.Sprintf("http://%s:%d", be.Name, be.Port)
		for _, fe := range services {
			if fe.IsWorker || isBackend(fe) {
				continue
			}
			if fe.Env == nil {
				fe.Env = map[string]string{}
			}
			for _, key := range []string{"NEXT_PUBLIC_API_URL", "VITE_API_URL", "REACT_APP_API_URL", "PUBLIC_API_URL"} {
				v := fe.Env[key]
				if v == "" || strings.Contains(v, "localhost") || strings.Contains(v, "127.0.0.1") || strings.Contains(v, "your_") || strings.Contains(v, "<") {
					fe.Env[key] = publicURL
				}
			}
			for _, key := range []string{"API_URL", "BACKEND_URL", "SERVER_URL"} {
				v := fe.Env[key]
				if v == "" || strings.Contains(v, "localhost") || strings.Contains(v, "127.0.0.1") || strings.Contains(v, "your_") || strings.Contains(v, "<") {
					fe.Env[key] = internalURL
				}
			}
		}
	}
	return nil
}

func isBackend(svc *domain.Service) bool {
	switch svc.Framework {
	case domain.FrameworkFastAPI, domain.FrameworkExpress, domain.FrameworkFastify, domain.FrameworkNestJS,
		domain.FrameworkDjango, domain.FrameworkFlask, domain.FrameworkGo, domain.FrameworkRust, domain.FrameworkKoa:
		return true
	}
	name := strings.ToLower(svc.Name)
	return strings.Contains(name, "api") || strings.Contains(name, "backend") || strings.Contains(name, "server")
}

var (
	portFlagRe      = regexp.MustCompile(`--port[=\s]+\d+`)
	hostFlagRe      = regexp.MustCompile(`--host[=\s]+[^\s]+`)
	runserverRe     = regexp.MustCompile(`runserver(?:\s+[^\s]+)?`)
	nextPortRe      = regexp.MustCompile(`(next\s+(?:start|dev))(?:\s+-p\s+\d+)?`)
	streamlitPortRe = regexp.MustCompile(`--server\.port[=\s]+\d+`)
)

// RewriteStartCommand makes a framework bind to the assigned container port.
func RewriteStartCommand(cmd string, fw domain.Framework, port int) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || port <= 0 {
		return cmd
	}
	switch fw {
	case domain.FrameworkFastAPI:
		cmd = ensureFlag(cmd, portFlagRe, fmt.Sprintf("--port %d", port))
		cmd = ensureFlag(cmd, hostFlagRe, "--host 0.0.0.0")
	case domain.FrameworkDjango:
		if runserverRe.MatchString(cmd) {
			cmd = runserverRe.ReplaceAllString(cmd, fmt.Sprintf("runserver 0.0.0.0:%d", port))
		} else {
			cmd += fmt.Sprintf(" 0.0.0.0:%d", port)
		}
	case domain.FrameworkFlask:
		cmd = ensureFlag(cmd, portFlagRe, fmt.Sprintf("--port=%d", port))
		cmd = ensureFlag(cmd, hostFlagRe, "--host=0.0.0.0")
	case domain.FrameworkVite, domain.FrameworkAstro:
		if strings.Contains(cmd, "vite") || strings.Contains(cmd, "astro") || strings.Contains(cmd, "dev") {
			cmd = ensureFlag(cmd, portFlagRe, fmt.Sprintf("--port %d", port))
			cmd = ensureFlag(cmd, hostFlagRe, "--host 0.0.0.0")
		}
	case domain.FrameworkStreamlit:
		cmd = ensureFlag(cmd, streamlitPortRe, fmt.Sprintf("--server.port %d", port))
		if !strings.Contains(cmd, "--server.address") {
			cmd += " --server.address 0.0.0.0"
		}
	case domain.FrameworkNextJS:
		if regexp.MustCompile(`-p\s+\d+`).MatchString(cmd) {
			return cmd
		}
		if strings.Contains(cmd, "next") {
			cmd = nextPortRe.ReplaceAllString(cmd, fmt.Sprintf("$1 -p %d", port))
		} else if strings.Contains(cmd, " run ") {
			cmd = strings.TrimSpace(cmd) + fmt.Sprintf(" -- -p %d", port)
		}
	}
	return cmd
}

func ensureFlag(cmd string, re *regexp.Regexp, flag string) string {
	if re.MatchString(cmd) {
		return re.ReplaceAllString(cmd, flag)
	}
	return strings.TrimSpace(cmd + " " + flag)
}
