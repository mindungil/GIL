package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var gitSummaryCacheTTL = 2 * time.Second

type gitSummaryCacheEntry struct {
	summary string
	expires time.Time
}

var gitSummaryCache struct {
	mu sync.Mutex
	m  map[string]gitSummaryCacheEntry
}

func gitWorkspaceSummary(ctx context.Context, workdir string) string {
	return gitWorkspaceSummaryAt(ctx, workdir, time.Now())
}

func gitWorkspaceSummaryAt(ctx context.Context, workdir string, now time.Time) string {
	if strings.TrimSpace(workdir) == "" {
		return ""
	}
	gitSummaryCache.mu.Lock()
	if gitSummaryCache.m == nil {
		gitSummaryCache.m = make(map[string]gitSummaryCacheEntry)
	}
	if entry, ok := gitSummaryCache.m[workdir]; ok && now.Before(entry.expires) {
		gitSummaryCache.mu.Unlock()
		return entry.summary
	}
	gitSummaryCache.mu.Unlock()

	output := gitCommandOutput(ctx, workdir, "status", "--porcelain=v1", "--branch")
	lines := strings.Split(output, "\n")
	branch := ""
	if len(lines) > 0 {
		head := strings.TrimSpace(lines[0])
		branch = strings.TrimPrefix(head, "## ")
		if idx := strings.Index(branch, "..."); idx >= 0 {
			branch = branch[:idx]
		}
		branch = strings.TrimSpace(branch)
	}
	dirty := 0
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) != "" {
			dirty++
		}
	}
	summary := ""
	if branch == "" {
		summary = ""
	} else if dirty == 0 {
		summary = fmt.Sprintf("%s · clean", branch)
	} else {
		summary = fmt.Sprintf("%s · dirty (%d files)", branch, dirty)
	}

	gitSummaryCache.mu.Lock()
	gitSummaryCache.m[workdir] = gitSummaryCacheEntry{
		summary: summary,
		expires: now.Add(gitSummaryCacheTTL),
	}
	gitSummaryCache.mu.Unlock()
	return summary
}

func gitCommandOutput(ctx context.Context, workdir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", workdir}, args...)...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &bytes.Buffer{}
	if err := cmd.Run(); err != nil {
		return ""
	}
	return stdout.String()
}

func clearGitSummaryCache() {
	gitSummaryCache.mu.Lock()
	defer gitSummaryCache.mu.Unlock()
	gitSummaryCache.m = nil
}
