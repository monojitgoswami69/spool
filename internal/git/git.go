// Package git parses repository URLs (including branch/subdir forms) and
// performs shallow clones. A GitHub PAT is injected through git's environment
// configuration so it never appears in argv, process listings, or the cloned
// repository's .git/config.
package git

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/monojitgoswami69/spool/internal/domain"
)

var (
	httpsRepoRe = regexp.MustCompile(`^https?://(?:www\.)?github\.com/([A-Za-z0-9][A-Za-z0-9_.-]*)/([A-Za-z0-9][A-Za-z0-9_.-]*?)(?:\.git)?/?$`)
	httpsTreeRe = regexp.MustCompile(`^https?://(?:www\.)?github\.com/([A-Za-z0-9][A-Za-z0-9_.-]*)/([A-Za-z0-9][A-Za-z0-9_.-]*?)(?:\.git)?/tree/([^/]+)(?:/(.*))?$`)
	sshRepoRe   = regexp.MustCompile(`^git@github\.com:([A-Za-z0-9][A-Za-z0-9_.-]*)/([A-Za-z0-9][A-Za-z0-9_.-]*?)(?:\.git)?$`)
	sshURLRe    = regexp.MustCompile(`^ssh://git@github\.com/([A-Za-z0-9][A-Za-z0-9_.-]*)/([A-Za-z0-9][A-Za-z0-9_.-]*?)(?:\.git)?/?$`)
)

// ParsedURL is a normalized GitHub repository reference.
type ParsedURL struct {
	Owner  string `json:"owner"`
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	Subdir string `json:"subdir,omitempty"`
	Clone  string `json:"clone"` // canonical https clone URL
	SSH    bool   `json:"ssh"`
}

// ParseURL parses https, /tree/<branch>[/<subdir>], git@ SSH, and ssh:// URLs.
func ParseURL(raw string) (*ParsedURL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, domain.E(domain.KindValidation, "git.ParseURL", "empty repository URL", nil)
	}
	s = strings.TrimSuffix(s, "/")

	if m := httpsTreeRe.FindStringSubmatch(s); m != nil {
		return &ParsedURL{
			Owner:  m[1],
			Repo:   strings.TrimSuffix(m[2], ".git"),
			Branch: m[3],
			Subdir: strings.Trim(m[4], "/"),
			Clone:  fmt.Sprintf("https://github.com/%s/%s.git", m[1], strings.TrimSuffix(m[2], ".git")),
		}, nil
	}
	if m := httpsRepoRe.FindStringSubmatch(s); m != nil {
		return &ParsedURL{
			Owner: m[1],
			Repo:  strings.TrimSuffix(m[2], ".git"),
			Clone: fmt.Sprintf("https://github.com/%s/%s.git", m[1], strings.TrimSuffix(m[2], ".git")),
		}, nil
	}
	if m := sshRepoRe.FindStringSubmatch(s); m != nil {
		return &ParsedURL{
			Owner: m[1],
			Repo:  strings.TrimSuffix(m[2], ".git"),
			Clone: s,
			SSH:   true,
		}, nil
	}
	if m := sshURLRe.FindStringSubmatch(s); m != nil {
		return &ParsedURL{
			Owner: m[1],
			Repo:  strings.TrimSuffix(m[2], ".git"),
			Clone: s,
			SSH:   true,
		}, nil
	}
	return nil, domain.E(domain.KindValidation, "git.ParseURL", "unsupported repository URL (expected a GitHub https or ssh URL): "+raw, nil)
}

// IsRemote reports whether s looks like a remote git URL rather than a local path.
func IsRemote(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "http://") ||
		strings.HasPrefix(t, "https://") ||
		strings.HasPrefix(t, "git@") ||
		strings.HasPrefix(t, "ssh://") ||
		strings.HasPrefix(t, "git://")
}

// ExtractRepoName returns the repository name, or "" when unparseable.
func ExtractRepoName(s string) string {
	if p, err := ParseURL(s); err == nil {
		return p.Repo
	}
	return ""
}

// Clone performs a shallow clone into targetDir. branch may be empty on the
// returned ParsedURL. The PAT is applied via GIT_CONFIG_* environment variables,
// never argv, and is scrubbed from any error text.
func Clone(ctx context.Context, repoURL, targetDir, pat, branch string) error {
	parsed, err := ParseURL(repoURL)
	if err != nil {
		return err
	}
	if branch == "" {
		branch = parsed.Branch
	}
	if targetDir == "" {
		return domain.E(domain.KindValidation, "git.Clone", "empty target directory", nil)
	}
	if entries, err := os.ReadDir(targetDir); err == nil && len(entries) > 0 {
		return domain.E(domain.KindValidation, "git.Clone", "target directory is not empty: "+targetDir, nil)
	}

	args := []string{"clone", "--depth=1"}
	if branch != "" {
		args = append(args, "--branch", branch, "--single-branch")
	}
	args = append(args, parsed.Clone, targetDir)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if pat = strings.TrimSpace(pat); pat != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + pat))
		cmd.Env = append(cmd.Env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+auth,
		)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := ScrubToken(string(out), pat)
		if strings.TrimSpace(msg) == "" {
			msg = ScrubToken(err.Error(), pat)
		}
		return domain.E(domain.KindDocker, "git.Clone", "git clone failed: "+strings.TrimSpace(msg), err)
	}
	return nil
}

// ScrubToken removes a secret from arbitrary output.
func ScrubToken(s, token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return s
	}
	s = strings.ReplaceAll(s, token, "***")
	// Also scrub the base64 form that could appear in verbose git output.
	b64 := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	s = strings.ReplaceAll(s, b64, "***")
	return s
}

// CountFiles reports the number of files and total bytes, excluding .git and
// common dependency directories.
func CountFiles(dir string) (int, int64, error) {
	var count int
	var bytes int64
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "dist", "build", "target", ".venv", "venv", "__pycache__":
				if path != dir {
					return filepath.SkipDir
				}
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		count++
		bytes += info.Size()
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return count, bytes, err
	}
	return count, bytes, nil
}
