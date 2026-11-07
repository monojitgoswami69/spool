package generator

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
	"gopkg.in/yaml.v3"
)

type composeFile struct {
	Services map[string]composeService `yaml:"services"`
	Volumes  map[string]composeVolume  `yaml:"volumes,omitempty"`
	Networks map[string]composeNetwork `yaml:"networks,omitempty"`
}

type composeVolume struct{}

type composeNetwork struct {
	Driver string `yaml:"driver,omitempty"`
}

type composeBuild struct {
	Context    string            `yaml:"context"`
	Dockerfile string            `yaml:"dockerfile"`
	Args       map[string]string `yaml:"args,omitempty"`
}

type composeDependency struct {
	Condition string `yaml:"condition"`
}

type composeHealthcheck struct {
	Test        []string `yaml:"test"`
	Interval    string   `yaml:"interval,omitempty"`
	Timeout     string   `yaml:"timeout,omitempty"`
	Retries     int      `yaml:"retries,omitempty"`
	StartPeriod string   `yaml:"start_period,omitempty"`
}

type composeService struct {
	Build         *composeBuild                `yaml:"build,omitempty"`
	Image         string                       `yaml:"image,omitempty"`
	ContainerName string                       `yaml:"container_name,omitempty"`
	Ports         []string                     `yaml:"ports,omitempty"`
	Environment   map[string]string            `yaml:"environment,omitempty"`
	EnvFile       []string                     `yaml:"env_file,omitempty"`
	Command       string                       `yaml:"command,omitempty"`
	Volumes       []string                     `yaml:"volumes,omitempty"`
	DependsOn     map[string]composeDependency `yaml:"depends_on,omitempty"`
	Healthcheck   *composeHealthcheck          `yaml:"healthcheck,omitempty"`
	Networks      []string                     `yaml:"networks,omitempty"`
	Restart       string                       `yaml:"restart,omitempty"`
	Init          bool                         `yaml:"init,omitempty"`
}

// GenerateComposeFile renders a portable compose file. build.context and
// build.dockerfile are relative to the compose file, so the workspace is
// relocatable.
func GenerateComposeFile(p *domain.Project, envFile string) (string, error) {
	if p == nil {
		return "", domain.E(domain.KindValidation, "generator.GenerateComposeFile", "nil project", nil)
	}
	network := p.NetworkName
	if network == "" {
		network = "spool-net"
	}
	cf := composeFile{
		Services: make(map[string]composeService),
		Volumes:  make(map[string]composeVolume),
		Networks: map[string]composeNetwork{network: {Driver: "bridge"}},
	}

	var infraNames []string
	for _, infra := range p.InfraServices {
		if infra.Mode == domain.InfraModeExternal {
			continue
		}
		ports := []string{}
		if infra.HostPort > 0 && infra.Port > 0 {
			ports = append(ports, fmt.Sprintf("%d:%d", infra.HostPort, infra.Port))
		}
		svc := composeService{
			Image:         infra.Image,
			ContainerName: composeName(p.ID, infra.Name),
			Ports:         ports,
			Environment:   infra.Env,
			Command:       infra.Command,
			Volumes:       infra.Volumes,
			Networks:      []string{network},
			Restart:       "unless-stopped",
		}
		if infra.Healthcheck.Test != nil {
			svc.Healthcheck = &composeHealthcheck{
				Test:        infra.Healthcheck.Test,
				Interval:    infra.Healthcheck.Interval,
				Timeout:     infra.Healthcheck.Timeout,
				Retries:     infra.Healthcheck.Retries,
				StartPeriod: infra.Healthcheck.StartPeriod,
			}
		}
		for _, v := range infra.Volumes {
			if name, ok := namedVolume(v); ok {
				cf.Volumes[name] = composeVolume{}
			}
		}
		cf.Services[infra.Name] = svc
		infraNames = append(infraNames, infra.Name)
	}
	sort.Strings(infraNames)

	context := relFromWorkDir(p.WorkDir, p.SourcePath)
	for _, app := range p.Services {
		dockerfilePath := filepath.Join(p.WorkDir, "Dockerfile."+app.Name)
		dockerfile := relFromWorkDir(p.SourcePath, dockerfilePath)
		buildArgs := map[string]string{}
		for _, ev := range p.Env {
			if isPublicBuildKey(ev.Key) {
				buildArgs[ev.Key] = "${" + ev.Key + "}"
			}
		}
		svc := composeService{
			Build: &composeBuild{
				Context:    context,
				Dockerfile: dockerfile,
				Args:       buildArgs,
			},
			ContainerName: composeName(p.ID, app.Name),
			Networks:      []string{network},
			Restart:       "unless-stopped",
			Init:          true,
		}
		if envFile != "" {
			svc.EnvFile = []string{envFile}
		}
		if len(app.Env) > 0 {
			svc.Environment = app.Env
		}
		if !app.IsWorker && app.Port > 0 {
			host := app.HostPort
			if host == 0 {
				host = app.Port
			}
			svc.Ports = []string{fmt.Sprintf("%d:%d", host, app.Port)}
			svc.Healthcheck = &composeHealthcheck{
				Test:        []string{"CMD-SHELL", fmt.Sprintf("curl -fsS http://localhost:%d/ >/dev/null || exit 1", app.Port)},
				Interval:    "10s",
				Timeout:     "5s",
				Retries:     6,
				StartPeriod: "30s",
			}
		}
		if len(infraNames) > 0 {
			deps := make(map[string]composeDependency)
			for _, name := range infraNames {
				cond := "service_started"
				for _, infra := range p.InfraServices {
					if infra.Name == name && infra.Healthcheck.Test != nil {
						cond = "service_healthy"
						break
					}
				}
				deps[name] = composeDependency{Condition: cond}
			}
			svc.DependsOn = deps
		}
		cf.Services[app.Name] = svc
	}

	data, err := yaml.Marshal(cf)
	if err != nil {
		return "", domain.E(domain.KindValidation, "generator.GenerateComposeFile", "marshal compose", err)
	}
	return string(data), nil
}

func composeName(projectID, svc string) string {
	pid := projectID
	if pid == "" {
		pid = "spool"
	}
	return pid + "-" + svc
}

func namedVolume(v string) (string, bool) {
	parts := strings.SplitN(v, ":", 2)
	if len(parts) != 2 {
		return "", false
	}
	name := parts[0]
	if name == "" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "~") {
		return "", false
	}
	if strings.ContainsAny(name, `/\`) {
		return "", false
	}
	return name, true
}
