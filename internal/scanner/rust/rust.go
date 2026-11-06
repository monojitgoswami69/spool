// Package rust detects Rust binary services from Cargo.toml and src/main.rs.
package rust

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/scanner/fsutil"
	"github.com/pelletier/go-toml/v2"
)

type cargoToml struct {
	Package struct {
		Name    string `toml:"name"`
		Version string `toml:"version"`
		Edition string `toml:"edition"`
	} `toml:"package"`
	Bin []struct {
		Name string `toml:"name"`
		Path string `toml:"path"`
	} `toml:"bin"`
}

var bindPortRe = regexp.MustCompile(`(?:bind|listen|SocketAddr::from)\([^)]*:(\d{2,5})`)

// IsRustDir reports whether dir contains a Cargo.toml.
func IsRustDir(dir string) bool { return fsutil.Exists(filepath.Join(dir, "Cargo.toml")) }

// Detect builds a domain.Service for a Rust binary crate, or nil for libraries.
func Detect(relPath, dir string) *domain.Service {
	data, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		return nil
	}
	var cargo cargoToml
	if err := toml.Unmarshal(data, &cargo); err != nil {
		return nil
	}
	hasMain := fsutil.Exists(filepath.Join(dir, "src", "main.rs"))
	if !hasMain && len(cargo.Bin) == 0 {
		return nil
	}
	name := fsutil.SanitizeName(cargo.Package.Name)
	if name == "" || name == "." {
		name = fsutil.SanitizeName(filepath.Base(dir))
	}

	port := detectPort(dir)
	svc := &domain.Service{
		Name:           name,
		Path:           cleanRel(relPath),
		Language:       domain.LangRust,
		Framework:      domain.FrameworkRust,
		PackageManager: domain.PMCargo,
		DefaultPort:    port,
		Port:           port,
		HostPort:       port,
		BuildCmd:       "cargo build --release",
		StartCmd:       "./" + binName(cargo.Package.Name, cargo.Bin),
	}
	if exposed := fsutil.DockerfileExpose(dir); exposed > 0 {
		svc.DefaultPort = exposed
		svc.Port = exposed
		svc.HostPort = exposed
	}
	return svc
}

func binName(pkg string, bins []struct {
	Name string `toml:"name"`
	Path string `toml:"path"`
}) string {
	if len(bins) > 0 && bins[0].Name != "" {
		return bins[0].Name
	}
	return pkg
}

func detectPort(dir string) int {
	port := 8080
	_ = filepath.WalkDir(filepath.Join(dir, "src"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".rs") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if m := bindPortRe.FindStringSubmatch(string(data)); m != nil {
			n := 0
			for _, r := range m[1] {
				n = n*10 + int(r-'0')
			}
			if n > 0 && n <= 65535 {
				port = n
			}
			return filepath.SkipAll
		}
		return nil
	})
	return port
}

func cleanRel(rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(rel))
}
