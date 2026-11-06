package domain

import (
	"errors"
	"fmt"
)

// ErrorKind is a stable, machine-classifiable error category.
type ErrorKind string

const (
	KindConfig     ErrorKind = "config"
	KindNotFound   ErrorKind = "not_found"
	KindUnsafePath ErrorKind = "unsafe_path"
	KindDocker     ErrorKind = "docker"
	KindValidation ErrorKind = "validation"
	KindLLM        ErrorKind = "llm"
	KindHealth     ErrorKind = "health"
	KindBudget     ErrorKind = "budget"
	KindCancelled  ErrorKind = "cancelled"
	KindIO         ErrorKind = "io"
)

// Error is Spool's typed error. It carries a stable kind, the operation that
// failed, and an optional wrapped cause so callers can branch on Kind while
// preserving the full chain for debugging.
type Error struct {
	Kind ErrorKind
	Op   string
	Msg  string
	Err  error
}

func (e *Error) Error() string {
	switch {
	case e.Op != "" && e.Msg != "":
		return fmt.Sprintf("%s: %s: %s", e.Op, e.Msg, e.Kind)
	case e.Op != "":
		return fmt.Sprintf("%s: %s", e.Op, e.Kind)
	case e.Msg != "":
		return fmt.Sprintf("%s: %s", e.Msg, e.Kind)
	default:
		return string(e.Kind)
	}
}

func (e *Error) Unwrap() error { return e.Err }

// E builds a typed error.
func E(kind ErrorKind, op, msg string, err error) *Error {
	return &Error{Kind: kind, Op: op, Msg: msg, Err: err}
}

// KindOf returns the ErrorKind of err, walking the wrap chain.
func KindOf(err error) (ErrorKind, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind, true
	}
	return "", false
}

// IsKind reports whether err (or anything it wraps) is of kind k.
func IsKind(err error, k ErrorKind) bool {
	got, ok := KindOf(err)
	return ok && got == k
}

// Sentinel errors used across packages.
var (
	ErrNoProjects      = errors.New("no spool projects found")
	ErrUnsafePath      = errors.New("path escapes the permitted root")
	ErrPortUnavailable = errors.New("no free port available")
	ErrNotConfigured   = errors.New("LLM provider is not configured")
	ErrBudgetExceeded  = errors.New("agent budget exceeded")
	ErrUnfixable       = errors.New("failure is not automatically repairable")
)

// UnknownProjectError is returned when a requested project cannot be resolved.
type UnknownProjectError struct {
	Name      string
	Available []string
}

func (e *UnknownProjectError) Error() string {
	if len(e.Available) == 0 {
		return fmt.Sprintf("unknown project %q (no projects have been initialized)", e.Name)
	}
	return fmt.Sprintf("unknown project %q (available: %v)", e.Name, e.Available)
}

// MissingLockError is returned when a project directory exists without a lock.
type MissingLockError struct{ Project string }

func (e *MissingLockError) Error() string {
	return fmt.Sprintf("project %q is missing its spool.lock.json (re-run `spool init`)", e.Project)
}
