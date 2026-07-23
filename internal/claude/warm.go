package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Warmer runs the claude binary once against whatever login is currently
// live, so Claude Code refreshes the on-disk credentials as a side effect.
// Injectable (like ExecRunner) so tests never spawn a process or reach the
// network.
//
// ccswitch itself still makes no network call: the official binary does, the
// same way the darwin keychain path delegates to security(1).
type Warmer func(ctx context.Context, model, prompt string) error

// RealWarmer runs `claude --model <model> --print <prompt>`, resolving claude
// through $PATH. Only the exit status matters, so stdout — the model's reply —
// is discarded rather than captured; stderr is kept for diagnostics.
func RealWarmer(ctx context.Context, model, prompt string) error {
	cmd := exec.CommandContext(ctx, "claude", "--model", model, "--print", prompt)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// Killing claude on a deadline is not enough: any grandchild it spawned
	// inherits the stderr pipe, and Wait blocks until that pipe closes. Without
	// a WaitDelay the timeout bounds nothing — the run lasts as long as the
	// slowest orphan. This forces the pipes shut shortly after the kill.
	cmd.WaitDelay = 2 * time.Second
	err := cmd.Run()
	if err == nil {
		return nil
	}
	// A killed process reports a generic "signal: killed"; the context says
	// what actually happened.
	if ctxErr := ctx.Err(); errors.Is(ctxErr, context.DeadlineExceeded) {
		return errors.New("timed out")
	} else if ctxErr != nil {
		return ctxErr
	}
	if msg := firstLine(stderr.String()); msg != "" {
		return fmt.Errorf("claude: %s: %w", msg, err)
	}
	return fmt.Errorf("claude: %w", err)
}

// firstLine trims output down to its first non-empty line, so a multi-line
// stderr dump stays readable inside a one-line-per-account report.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
