package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monojitgoswami69/spool/internal/domain"
	"github.com/monojitgoswami69/spool/internal/healer"
	"github.com/monojitgoswami69/spool/internal/safefs"
	"github.com/monojitgoswami69/spool/internal/state"
	"github.com/monojitgoswami69/spool/internal/ui"
)

// printFailureReport turns a failed init/heal into an actionable summary.
func printFailureReport(mgr *state.Manager, lock *domain.Lock, res *healer.Result, err error) {
	logf("")
	logf("%s", ui.ErrorLine("stack could not be brought up"))
	if mgr != nil && res != nil {
		writeFailureRecord(mgr, res)
	}
	if res != nil {
		for _, a := range res.Attempts {
			logf("  attempt %d [%s] %s", a.N, a.Kind, a.Summary)
		}
		if strings.TrimSpace(res.Summary) != "" {
			logf("  %s", res.Summary)
		}
		if tail := strings.TrimSpace(healer.Tail(res.FinalLog, 15)); tail != "" {
			logf("")
			logf("%s", ui.Section("last build/runtime output"))
			logf("%s", tail)
		}
	}
	if err != nil {
		verbosef("underlying error: %v", err)
	}
	logf("")
	logf("%s", ui.Section("next steps"))
	id := ""
	if mgr != nil {
		id = mgr.ID
	} else if lock != nil {
		id = lock.ProjectID
	}
	for _, line := range []string{
		"spool logs " + id,
		"spool heal " + id,
		"spool env " + id,
		"spool explain " + id,
	} {
		logf("  %s", line)
	}
	logf("%s", ui.InfoLine("If no LLM is configured, set one with: spool config set api-key <key>"))
}

func writeFailureRecord(mgr *state.Manager, res *healer.Result) {
	var b strings.Builder
	b.WriteString("Spool failure record\n")
	b.WriteString("time: " + time.Now().UTC().Format(time.RFC3339) + "\n")
	b.WriteString("summary: " + res.Summary + "\n")
	for _, a := range res.Attempts {
		b.WriteString(fmt.Sprintf("attempt %d [%s] %s\n", a.N, a.Kind, a.Summary))
	}
	if strings.TrimSpace(res.FinalLog) != "" {
		b.WriteString("\n--- last build/runtime output ---\n")
		b.WriteString(res.FinalLog)
		b.WriteString("\n")
	}
	_ = safefs.WriteFileAtomic(filepath.Join(mgr.Dir, "last-failure.log"), []byte(b.String()), 0o600)
}

func clearFailureLog(mgr *state.Manager) {
	if mgr == nil {
		return
	}
	_ = os.Remove(filepath.Join(mgr.Dir, "last-failure.log"))
}
