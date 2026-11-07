// Package generator renders hermetic multi-stage Dockerfiles and a portable
// docker-compose.yml. Build contexts are always expressed relative to the
// compose file so the generated workspace can be moved between machines.
package generator

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/safefs"
)

// Output is the set of generated files keyed by filename.
type Output struct {
	Files       map[string]string `json:"files"`
	WorkDir     string            `json:"work_dir"`
	ComposePath string            `json:"compose_path"`
}

// Options tunes generation and is derived from the project when empty.
type Options struct {
	RepoRoot string
	WorkDir  string
	Monorepo bool
	// BuildEnv holds public build-time variables (NEXT_PUBLIC_*, VITE_*,
	// REACT_APP_*) that must be present while the frontend is compiled.
	BuildEnv map[string]string
	// ServiceContext is set when this service builds from its own directory
	// rather than the repository root.
	ServiceContext bool
	ContextRoot    string
}

// Generate produces all manifests in memory.
func Generate(p *domain.Project) (*Output, error) {
	return GenerateWithOptions(p, Options{
		RepoRoot: p.SourcePath,
		WorkDir:  p.WorkDir,
		Monorepo: detectMonorepo(p.SourcePath),
	})
}

// GenerateWithOptions produces all manifests with explicit options.
func GenerateWithOptions(p *domain.Project, opts Options) (*Output, error) {
	if p == nil {
		return nil, domain.E(domain.KindValidation, "generator.Generate", "nil project", nil)
	}
	if opts.RepoRoot == "" {
		opts.RepoRoot = p.SourcePath
	}
	if opts.WorkDir == "" {
		opts.WorkDir = p.WorkDir
	}
	files := make(map[string]string)
	if opts.BuildEnv == nil {
		opts.BuildEnv = publicBuildEnv(p)
	}

	monorepo := opts.Monorepo
	for _, svc := range p.Services {
		svcOpts := opts
		if independentNodeContext(svc, monorepo) {
			svcOpts.ServiceContext = true
			svcOpts.ContextRoot = filepath.Join(opts.RepoRoot, filepath.FromSlash(svc.Path))
		} else {
			svcOpts.ServiceContext = false
			svcOpts.ContextRoot = opts.RepoRoot
		}
		files["Dockerfile."+svc.Name] = GenerateDockerfile(svc, svcOpts)
	}

	compose, err := GenerateComposeFile(p, ".env")
	if err != nil {
		return nil, err
	}
	files["docker-compose.yml"] = compose
	files[".dockerignore"] = DockerignoreContent(excludedNodePaths(p, monorepo)...)

	return &Output{Files: files, WorkDir: opts.WorkDir, ComposePath: filepath.Join(opts.WorkDir, "docker-compose.yml")}, nil
}

// WriteOutputs generates manifests and writes them atomically inside workDir.
// It also writes a .dockerignore to the repository root when one is absent and
// points every service's DockerfilePath at the generated file.
func WriteOutputs(p *domain.Project, workDir, repoRoot string) (*Output, error) {
	if workDir == "" {
		workDir = p.WorkDir
	}
	if err := safefs.EnsureDir(workDir, 0o755); err != nil {
		return nil, domain.E(domain.KindIO, "generator.WriteOutputs", "create work dir", err)
	}
	out, err := GenerateWithOptions(p, Options{
		RepoRoot: repoRoot,
		WorkDir:  workDir,
		Monorepo: detectMonorepo(repoRoot),
	})
	if err != nil {
		return nil, err
	}

	jail, err := safefs.NewJail(workDir)
	if err != nil {
		return nil, domain.E(domain.KindIO, "generator.WriteOutputs", "jail work dir", err)
	}
	for name, content := range out.Files {
		if name == ".dockerignore" {
			continue
		}
		if err := jail.WriteFile(name, []byte(content), 0o644); err != nil {
			return nil, domain.E(domain.KindIO, "generator.WriteOutputs", "write "+name, err)
		}
	}
	for _, svc := range p.Services {
		svc.DockerfilePath = filepath.Join(workDir, "Dockerfile."+svc.Name)
	}

	// Emit a managed .dockerignore block. User content is preserved; only the
	// block between the markers is rewritten.
	if repoRoot != "" {
		if repoJail, jerr := safefs.NewJail(repoRoot); jerr == nil {
			_ = repoJail.WriteFile(".dockerignore", []byte(mergeDockerignore(readFileOrEmpty(repoRoot, ".dockerignore"), excludedNodePaths(p, detectMonorepo(repoRoot)))), 0o644)
		}
	}
	return out, nil
}

const (
	ignoreStart = "# spool:managed-start"
	ignoreEnd   = "# spool:managed-end"
)

func readFileOrEmpty(root, name string) string {
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		return ""
	}
	return string(data)
}

func mergeDockerignore(existing string, excludes []string) string {
	var user []string
	inBlock := false
	for _, line := range strings.Split(existing, "\n") {
		if strings.TrimSpace(line) == ignoreStart {
			inBlock = true
			continue
		}
		if strings.TrimSpace(line) == ignoreEnd {
			inBlock = false
			continue
		}
		if !inBlock {
			user = append(user, line)
		}
	}
	base := strings.TrimRight(strings.Join(user, "\n"), "\n")
	if base == "" {
		base = defaultDockerignore()
	}
	if len(excludes) == 0 {
		return base + "\n"
	}
	var b strings.Builder
	b.WriteString(base)
	b.WriteString("\n\n" + ignoreStart + "\n")
	b.WriteString("# generated by spool; nested independent projects are excluded from the root build context\n")
	for _, e := range excludes {
		b.WriteString(e + "\n")
	}
	b.WriteString(ignoreEnd + "\n")
	return b.String()
}

// DockerignoreContent is the managed build-context ignore file.
func DockerignoreContent(excludes ...string) string {
	out := defaultDockerignore()
	if len(excludes) == 0 {
		return out
	}
	var b strings.Builder
	b.WriteString(out)
	b.WriteString("\n" + ignoreStart + "\n")
	b.WriteString("# generated by spool; nested independent projects are excluded from the root build context\n")
	for _, e := range excludes {
		b.WriteString(e + "\n")
	}
	b.WriteString(ignoreEnd + "\n")
	return b.String()
}

func defaultDockerignore() string {
	return `.git
.gitignore
**/node_modules
**/dist
**/build
**/.next
**/.nuxt
**/.svelte-kit
**/target
**/.venv
**/venv
**/__pycache__
**/*.tsbuildinfo
**/.turbo
**/.cache
**/*.log
.env
.env.*
!.env.example
`
}

// independentNodeContext reports whether svc is a nested Node app that should
// build from its own directory instead of the repository root.
func independentNodeContext(svc *domain.Service, monorepo bool) bool {
	if svc == nil || svc.Language != domain.LangNode || monorepo {
		return false
	}
	if svc.WorkspaceMember || svc.Path == "" || svc.Path == "." {
		return false
	}
	return true
}

func serviceContextRoot(svc *domain.Service, repoRoot string, monorepo bool) string {
	if independentNodeContext(svc, monorepo) {
		return filepath.Join(repoRoot, filepath.FromSlash(svc.Path))
	}
	return repoRoot
}

func excludedNodePaths(p *domain.Project, monorepo bool) []string {
	if p == nil || monorepo {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, svc := range p.Services {
		if !independentNodeContext(svc, monorepo) {
			continue
		}
		if svc.Path != "" && !seen[svc.Path] {
			seen[svc.Path] = true
			out = append(out, svc.Path)
		}
	}
	sort.Strings(out)
	return out
}

func detectMonorepo(repoRoot string) bool {
	if repoRoot == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "pnpm-workspace.yaml")); err == nil {
		return true
	}
	if data, err := os.ReadFile(filepath.Join(repoRoot, "package.json")); err == nil {
		text := string(data)
		return strings.Contains(text, `"workspaces"`)
	}
	return false
}

func serviceDir(svc *domain.Service) string {
	if svc.Path == "" || svc.Path == "." {
		return "."
	}
	return filepath.ToSlash(svc.Path)
}

func relFromWorkDir(workDir, target string) string {
	if workDir == "" || target == "" {
		return "."
	}
	rel, err := filepath.Rel(workDir, target)
	if err != nil {
		return "."
	}
	return filepath.ToSlash(rel)
}

func existsFile(root, rel string) bool {
	if root == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	return err == nil
}

func fmtPort(p int) int {
	if p <= 0 {
		return 80
	}
	return p
}

// publicBuildEnv selects frontend variables that are safe and necessary at
// image build time.
func publicBuildEnv(p *domain.Project) map[string]string {
	out := map[string]string{}
	for _, ev := range p.Env {
		if !isPublicBuildKey(ev.Key) {
			continue
		}
		val := ev.Value
		if val == "" {
			val = ev.Default
		}
		out[ev.Key] = val
	}
	return out
}

func isPublicBuildKey(k string) bool {
	return strings.HasPrefix(k, "NEXT_PUBLIC_") || strings.HasPrefix(k, "VITE_") || strings.HasPrefix(k, "REACT_APP_")
}

func sortedKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
