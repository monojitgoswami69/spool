package safefs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestJailReadWriteRoundTrip(t *testing.T) {
	root := t.TempDir()
	j, err := NewJail(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.WriteFile("sub/hello.txt", []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := j.ReadFile("sub/hello.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hi" {
		t.Fatalf("got %q", got)
	}
}

func TestJailRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	j, _ := NewJail(root)
	for _, rel := range []string{"../escape.txt", "../../../../etc/passwd", "a/../../b"} {
		if _, err := j.Resolve(rel); err == nil {
			t.Fatalf("expected escape rejection for %q", rel)
		}
	}
}

func TestJailRejectsAbsoluteOutside(t *testing.T) {
	root := t.TempDir()
	j, _ := NewJail(root)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.ReadFile(outside); err == nil {
		t.Fatal("expected rejection of absolute outside path")
	}
}

func TestJailRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	j, _ := NewJail(root)
	if _, err := j.ReadFile(filepath.Join("escape", "secret.txt")); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}

func TestJailWriteSecretPerms(t *testing.T) {
	root := t.TempDir()
	j, _ := NewJail(root)
	if err := j.WriteSecret(".env", []byte("KEY=value\n")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("expected 0600, got %o", mode)
	}
}

func TestJailReadLimit(t *testing.T) {
	root := t.TempDir()
	j, _ := NewJail(root)
	if err := j.WriteFile("big.txt", []byte(strings.Repeat("a", 5000)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := j.ReadFileLimited("big.txt", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 {
		t.Fatalf("expected 100 bytes, got %d", len(got))
	}
}

func TestWriteFileAtomicReplacesExisting(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(p, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(p, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != "new" {
		t.Fatalf("got %q", got)
	}
}

func TestSafeRemoveAll(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "project")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SafeRemoveAll(child, root); err != nil {
		t.Fatalf("expected removal to succeed: %v", err)
	}
	if _, err := os.Stat(child); !os.IsNotExist(err) {
		t.Fatal("child should be gone")
	}

	outside := filepath.Join(t.TempDir(), "keep")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SafeRemoveAll(outside, root); err == nil {
		t.Fatal("expected refusal outside allowed roots")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("outside dir should still exist")
	}
}

func TestSafeRemoveAllRefusesDangerousPaths(t *testing.T) {
	for _, p := range []string{"", "/", "."} {
		if err := SafeRemoveAll(p, t.TempDir()); err == nil {
			t.Fatalf("expected refusal for %q", p)
		}
	}
}

func TestIsSubPath(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "a", "b")
	ok, err := IsSubPath(root, inside)
	if err != nil || !ok {
		t.Fatalf("expected inside, got %v %v", ok, err)
	}
	ok, _ = IsSubPath(root, filepath.Dir(root))
	if ok {
		t.Fatal("parent must not be a subpath")
	}
}

func TestErrEscapeIdentity(t *testing.T) {
	root := t.TempDir()
	j, _ := NewJail(root)
	_, err := j.Resolve("../x")
	if !errors.Is(err, ErrEscape) {
		t.Fatalf("expected ErrEscape, got %v", err)
	}
}
