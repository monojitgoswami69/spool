// Package safefs is Spool's filesystem safety boundary. Every path that an
// LLM-controlled agent or an untrusted build log can influence MUST be resolved
// through a Jail before any read, write, or delete. The jail resolves symlinks
// component-by-component, refuses escapes, and performs atomic writes.
package safefs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// MaxReadBytes caps a single jailed read (1 MiB).
const MaxReadBytes int64 = 1 << 20

// ErrEscape is returned when a path would leave the jail root.
var ErrEscape = errors.New("safefs: path escapes the permitted root")

// Jail confines all operations to a single resolved root directory.
type Jail struct {
	root string
	mu   sync.Mutex
}

// NewJail creates a jail rooted at dir. The directory must already exist and be
// a directory; its real (symlink-resolved) path becomes the containment root.
func NewJail(dir string) (*Jail, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("safefs: abs: %w", err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("safefs: resolve root: %w", err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return nil, fmt.Errorf("safefs: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("safefs: %s is not a directory", real)
	}
	return &Jail{root: real}, nil
}

// Root returns the resolved containment root.
func (j *Jail) Root() string { return j.root }

// Resolve maps a relative path to an absolute path guaranteed to live inside
// the jail. It resolves symlinks on the longest existing prefix, so a symlink
// pointing outside the jail is rejected even when the final target does not yet
// exist. Absolute paths and ".." are rejected.
func (j *Jail) Resolve(rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("%w: empty path", ErrEscape)
	}
	if filepath.IsAbs(rel) {
		// Allow an absolute path only if it is already inside the jail; this
		// keeps callers that pass absolute paths honest without weakening the
		// boundary.
		resolved, err := resolveExisting(rel)
		if err != nil {
			return "", err
		}
		if !within(j.root, resolved) {
			return "", fmt.Errorf("%w: %s", ErrEscape, rel)
		}
		return resolved, nil
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s", ErrEscape, rel)
	}
	target := filepath.Join(j.root, clean)
	resolved, err := resolveExisting(target)
	if err != nil {
		return "", err
	}
	if resolved != j.root && !within(j.root, resolved) {
		return "", fmt.Errorf("%w: %s", ErrEscape, rel)
	}
	return resolved, nil
}

// resolveExisting evaluates symlinks for the longest existing prefix of p and
// re-appends the not-yet-existing suffix. This prevents a symlinked parent from
// escaping the jail while still permitting writes of new files.
func resolveExisting(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	var suffix []string
	cur := abs
	for i := 0; i < 256; i++ {
		if _, err := os.Lstat(cur); err == nil {
			real, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return "", fmt.Errorf("safefs: resolve %s: %w", cur, err)
			}
			for k := len(suffix) - 1; k >= 0; k-- {
				real = filepath.Join(real, suffix[k])
			}
			return filepath.Clean(real), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(abs), nil
		}
		suffix = append(suffix, filepath.Base(cur))
		cur = parent
	}
	return "", fmt.Errorf("safefs: too many path components resolving %s", p)
}

// within reports whether target is parent or a descendant of parent. Both must
// be absolute, cleaned paths.
func within(parent, target string) bool {
	if target == parent {
		return true
	}
	rel, err := filepath.Rel(parent, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Exists reports whether rel exists inside the jail.
func (j *Jail) Exists(rel string) bool {
	p, err := j.Resolve(rel)
	if err != nil {
		return false
	}
	_, err = os.Lstat(p)
	return err == nil
}

// ReadFile reads a jailed file, capped at MaxReadBytes.
func (j *Jail) ReadFile(rel string) ([]byte, error) {
	return j.ReadFileLimited(rel, MaxReadBytes)
}

// ReadFileLimited reads a jailed file with an explicit byte cap.
func (j *Jail) ReadFileLimited(rel string, limit int64) ([]byte, error) {
	p, err := j.Resolve(rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if limit <= 0 {
		limit = MaxReadBytes
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return nil, err
	}
	return data, nil
}

// WriteFile atomically writes rel inside the jail with the given permission.
// The temporary file is created in the destination directory so the rename is
// atomic on POSIX filesystems.
func (j *Jail) WriteFile(rel string, data []byte, perm os.FileMode) error {
	p, err := j.Resolve(rel)
	if err != nil {
		return err
	}
	return WriteFileAtomic(p, data, perm)
}

// WriteSecret writes a file with owner-only permissions (0600) and rejects any
// broader mode, making it the canonical way to persist credentials.
func (j *Jail) WriteSecret(rel string, data []byte) error {
	return j.WriteFile(rel, data, 0o600)
}

// Remove deletes a single file inside the jail.
func (j *Jail) Remove(rel string) error {
	p, err := j.Resolve(rel)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// Open opens a jailed file for reading.
func (j *Jail) Open(rel string) (*os.File, error) {
	p, err := j.Resolve(rel)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

// WriteFileAtomic writes data to path via a same-directory temp file, fsyncs,
// then renames. It is safe against partial writes and concurrent readers.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("safefs: mkdir %s: %w", dir, err)
	}
	// Never allow group/other write bits on files we create.
	perm &^= 0o022
	tmp, err := os.CreateTemp(dir, ".spool-tmp-*")
	if err != nil {
		return fmt.Errorf("safefs: temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("safefs: write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("safefs: chmod %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("safefs: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("safefs: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("safefs: rename %s: %w", tmpName, err)
	}
	// Best-effort directory fsync for durability of the rename.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// IsSubPath reports whether target is parent or a descendant of parent, with
// symlinks resolved. It returns false for the parent itself only when strict is
// requested.
func IsSubPath(parent, target string) (bool, error) {
	pa, err := filepath.Abs(parent)
	if err != nil {
		return false, err
	}
	ta, err := filepath.Abs(target)
	if err != nil {
		return false, err
	}
	if rp, err := filepath.EvalSymlinks(pa); err == nil {
		pa = rp
	}
	if rt, err := filepath.EvalSymlinks(ta); err == nil {
		ta = rt
	} else {
		// Resolve the existing prefix so a symlinked ancestor cannot smuggle
		// the target outside the parent.
		if resolved, rerr := resolveExisting(ta); rerr == nil {
			ta = resolved
		}
	}
	return within(pa, ta), nil
}

// SafeRemoveAll deletes target only if it is strictly inside one of the allowed
// roots. It refuses the empty path, filesystem root, current directory, home
// directory, and any path outside every allowed root.
func SafeRemoveAll(target string, allowedRoots ...string) error {
	if strings.TrimSpace(target) == "" {
		return fmt.Errorf("%w: refusing to remove empty path", ErrEscape)
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if abs == string(filepath.Separator) || abs == "." {
		return fmt.Errorf("%w: refusing to remove %s", ErrEscape, abs)
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		if h, _ := filepath.Abs(home); abs == h {
			return fmt.Errorf("%w: refusing to remove home directory", ErrEscape)
		}
	}
	if len(allowedRoots) == 0 {
		return fmt.Errorf("%w: no allowed roots supplied", ErrEscape)
	}
	allowed := false
	for _, root := range allowedRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		// The target itself may equal a root only when it is a project clone
		// directory, never a global root like ~/.spool. We require strict
		// containment to stay conservative.
		ok, err := IsSubPath(root, abs)
		if err != nil {
			continue
		}
		if ok {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("%w: %s is outside allowed roots", ErrEscape, abs)
	}
	return os.RemoveAll(abs)
}

// EnsureDir creates dir (and parents) with perm, tightening the mode so the
// directory is never group/other writable.
func EnsureDir(dir string, perm os.FileMode) error {
	perm &^= 0o022
	return os.MkdirAll(dir, perm)
}
