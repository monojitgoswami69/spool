// Package domain holds Spool's shared vocabulary: pure data types, constants,
// interfaces, and the typed error taxonomy. It imports nothing from the rest of
// the application so every other package may depend on it freely.
package domain

import (
	"strings"
	"time"
)

// Language identifies the primary language of a service.
type Language string

const (
	LangNode   Language = "node"
	LangPython Language = "python"
	LangGo     Language = "go"
	LangRust   Language = "rust"
	LangStatic Language = "static"
	LangDocker Language = "docker"
)

// Framework identifies the web/app framework of a service.
type Framework string

const (
	FrameworkNextJS    Framework = "nextjs"
	FrameworkVite      Framework = "vite"
	FrameworkCRA       Framework = "cra"
	FrameworkRemix     Framework = "remix"
	FrameworkNuxt      Framework = "nuxt"
	FrameworkSvelteKit Framework = "sveltekit"
	FrameworkAstro     Framework = "astro"
	FrameworkExpress   Framework = "express"
	FrameworkFastify   Framework = "fastify"
	FrameworkNestJS    Framework = "nestjs"
	FrameworkKoa       Framework = "koa"

	FrameworkFastAPI   Framework = "fastapi"
	FrameworkDjango    Framework = "django"
	FrameworkFlask     Framework = "flask"
	FrameworkCelery    Framework = "celery"
	FrameworkStreamlit Framework = "streamlit"
	FrameworkGradio    Framework = "gradio"

	FrameworkGo      Framework = "go"
	FrameworkRust    Framework = "rust"
	FrameworkGeneric Framework = "generic"
	FrameworkStatic  Framework = "static"
	FrameworkDocker  Framework = "docker"
)

// PackageManager identifies the dependency manager in use.
type PackageManager string

const (
	PMNpm   PackageManager = "npm"
	PMPnpm  PackageManager = "pnpm"
	PMYarn  PackageManager = "yarn"
	PMBun   PackageManager = "bun"
	PMCargo PackageManager = "cargo"
	PMGoMod PackageManager = "gomod"

	PMPip    PackageManager = "pip"
	PMPoetry PackageManager = "poetry"
	PMUv     PackageManager = "uv"
	PMPipenv PackageManager = "pipenv"
)

// InfraType identifies a backing service.
type InfraType string

const (
	InfraPostgres      InfraType = "postgres"
	InfraRedis         InfraType = "redis"
	InfraMySQL         InfraType = "mysql"
	InfraMongo         InfraType = "mongo"
	InfraKafka         InfraType = "kafka"
	InfraMinIO         InfraType = "minio"
	InfraRabbitMQ      InfraType = "rabbitmq"
	InfraElasticsearch InfraType = "elasticsearch"
)

// InfraMode records whether a backing service is provisioned locally or is an
// externally managed cloud service that must be suppressed.
type InfraMode string

const (
	InfraModeLocal    InfraMode = "local"
	InfraModeExternal InfraMode = "external"
)

// EnvVarSource records where a discovered environment variable came from.
type EnvVarSource string

const (
	EnvSourceCode    EnvVarSource = "code"
	EnvSourceExample EnvVarSource = "example"
	EnvSourceSchema  EnvVarSource = "schema"
	EnvSourceInfra   EnvVarSource = "infra"
	EnvSourceUser    EnvVarSource = "user"
	EnvSourceAutoDev EnvVarSource = "auto_dev"
)

// Service is a detected runnable application service.
type Service struct {
	Name                  string            `json:"name"`
	Path                  string            `json:"path"` // relative to repository root
	Language              Language          `json:"language"`
	Framework             Framework         `json:"framework"`
	PackageManager        PackageManager    `json:"package_manager"`
	PackageManagerVersion string            `json:"package_manager_version,omitempty"`
	RuntimeVersion        string            `json:"runtime_version,omitempty"`
	BuildCmd              string            `json:"build_cmd,omitempty"`
	StartCmd              string            `json:"start_cmd"`
	DefaultPort           int               `json:"default_port"`
	Port                  int               `json:"port"`      // container-internal port
	HostPort              int               `json:"host_port"` // host-published port
	IsSPA                 bool              `json:"is_spa"`
	IsWorker              bool              `json:"is_worker"`
	Env                   map[string]string `json:"env,omitempty"`
	DependsOn             []string          `json:"depends_on,omitempty"`
	DockerfilePath        string            `json:"dockerfile_path,omitempty"`
	Dependencies          []string          `json:"dependencies,omitempty"`
	WorkspaceMember       bool              `json:"workspace_member,omitempty"`
	// ExistingDockerfile is a repo-relative Dockerfile found in the source.
	ExistingDockerfile string `json:"existing_dockerfile,omitempty"`
	// BuildContext is the repo-relative context for ExistingDockerfile.
	BuildContext string `json:"build_context,omitempty"`
}

// HasDependency reports whether the service declares a dependency (case-insensitive).
func (s *Service) HasDependency(name string) bool {
	for _, d := range s.Dependencies {
		if strings.EqualFold(d, name) {
			return true
		}
	}
	return false
}

// HealthcheckSpec describes a container health probe in compose terms.
type HealthcheckSpec struct {
	Test        []string `json:"test"`
	Interval    string   `json:"interval"`
	Timeout     string   `json:"timeout"`
	Retries     int      `json:"retries"`
	StartPeriod string   `json:"start_period,omitempty"`
}

// InfraService is an inferred backing service.
type InfraService struct {
	Type        InfraType         `json:"type"`
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	Port        int               `json:"port"`
	HostPort    int               `json:"host_port"`
	Env         map[string]string `json:"env,omitempty"`
	Command     string            `json:"command,omitempty"`
	Volumes     []string          `json:"volumes,omitempty"`
	Healthcheck HealthcheckSpec   `json:"healthcheck,omitempty"`
	Mode        InfraMode         `json:"mode,omitempty"`
	Provider    string            `json:"provider,omitempty"`
	Reason      string            `json:"reason,omitempty"`
}

// EnvVar is a discovered environment variable.
type EnvVar struct {
	Key         string       `json:"key"`
	Value       string       `json:"value,omitempty"`
	Default     string       `json:"default,omitempty"`
	Source      EnvVarSource `json:"source"`
	Required    bool         `json:"required"`
	Description string       `json:"description,omitempty"`
}

// RepoIntel is source-repository intelligence the agent can use before
// deciding whether to reuse existing Docker assets or generate new ones.
type RepoIntel struct {
	Readme       string            `json:"readme,omitempty"`
	Docs         []string          `json:"docs,omitempty"`
	Makefile     string            `json:"makefile,omitempty"`
	Dockerfiles  []string          `json:"dockerfiles,omitempty"`
	ComposeFiles []string          `json:"compose_files,omitempty"`
	Scripts      map[string]string `json:"scripts,omitempty"`
}

// Project is the unit of orchestration.
type Project struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	SourcePath    string          `json:"source_path"`
	IsLocal       bool            `json:"is_local"`
	WorkDir       string          `json:"work_dir"`
	Services      []*Service      `json:"services"`
	InfraServices []*InfraService `json:"infra_services"`
	Env           []*EnvVar       `json:"env,omitempty"`
	Intel         RepoIntel       `json:"intel,omitempty"`
	NetworkName   string          `json:"network_name,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// Container is a parsed `docker compose ps` row.
type Container struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Service  string `json:"service"`
	State    string `json:"state"`
	Status   string `json:"status"`
	Health   string `json:"health"`
	Image    string `json:"image"`
	Project  string `json:"project"`
	ExitCode int    `json:"exit_code"`
}

// ContainerStat is a parsed `docker stats` row.
type ContainerStat struct {
	Container string  `json:"container"`
	Name      string  `json:"name"`
	CPUPerc   float64 `json:"cpu_perc"`
	MemPerc   float64 `json:"mem_perc"`
	MemUsage  string  `json:"mem_usage"`
	NetIO     string  `json:"net_io"`
	BlockIO   string  `json:"block_io"`
	PIDs      string  `json:"pids"`
}

// RepairRecord is a single provenance entry written to repairs.json.
type RepairRecord struct {
	ID            string    `json:"id"`
	Timestamp     time.Time `json:"timestamp"`
	Service       string    `json:"service,omitempty"`
	File          string    `json:"file"`
	OriginalHash  string    `json:"original_hash"`
	ResultingHash string    `json:"resulting_hash"`
	FailureCat    string    `json:"failure_category,omitempty"`
	Operation     string    `json:"operation"`
	Summary       string    `json:"summary,omitempty"`
}

// Lock is the persisted project state written to ~/.spool/projects/<id>/spool.lock.json.
type Lock struct {
	Version       string          `json:"version"`
	Project       string          `json:"project"`
	ProjectID     string          `json:"project_id"`
	RepoURL       string          `json:"repo_url,omitempty"`
	RepoPath      string          `json:"repo_path"`
	WorkDir       string          `json:"work_dir"`
	Services      []*Service      `json:"services"`
	Infra         []*InfraService `json:"infra,omitempty"`
	PortMap       map[string]int  `json:"port_map,omitempty"`
	Env           []*EnvVar       `json:"env,omitempty"`
	Intel         RepoIntel       `json:"intel,omitempty"`
	NetworkName   string          `json:"network_name,omitempty"`
	DetectionHash string          `json:"detection_hash"`
	LastInit      time.Time       `json:"last_init"`
	LastUp        time.Time       `json:"last_up,omitempty"`
	PublicURL     string          `json:"public_url,omitempty"`
}
