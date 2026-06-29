package agent

import (
	"fmt"
	"strings"

	"reasonix/internal/provider"
)

// Tool-result maintenance is the free half of context management: stale tool
// results are re-derivable (files can be re-read, commands re-run), so rewriting
// them needs no summarizer call and never drops a message. tool_call/result
// pairing and assistant content (including signed reasoning) are untouched.
const (
	snippedMarker = "[snipped tool result — "
	prunedMarker  = "[elided tool result — "
	minPruneBytes = 1024
)

type toolResultMaintenanceMode int

const (
	toolResultSnip toolResultMaintenanceMode = iota
	toolResultPrune
)

// PruneStats reports one maintenance pass.
type PruneStats struct {
	Results    int
	SavedChars int
	Archive    string
}

// SnipStaleToolResults shortens stale tool-result content older than the
// protected recent tail, archiving the originals first. Idempotent; a no-op
// when compaction is disabled (no context window).
func (a *Agent) SnipStaleToolResults() (PruneStats, error) {
	return a.maintainStaleToolResults(toolResultSnip)
}

// PruneStaleToolResults elides stale tool-result content older than the
// protected recent tail, archiving the originals first. It can upgrade already
// snipped results to a shorter placeholder.
func (a *Agent) PruneStaleToolResults() (PruneStats, error) {
	return a.maintainStaleToolResults(toolResultPrune)
}

func (a *Agent) maintainStaleToolResults(mode toolResultMaintenanceMode) (PruneStats, error) {
	var st PruneStats
	if a.contextWindow <= 0 {
		return st, nil
	}
	msgs := a.session.Messages
	head, start, ok := a.planCompaction(msgs, 1)
	if !ok {
		if mode != toolResultPrune {
			return st, nil
		}
		head = 1
		start = len(msgs) - a.recentKeep
		if start < head {
			return st, nil
		}
	}
	var idx []int
	for i := head; i < start; i++ {
		m := msgs[i]
		if !shouldMaintainToolResult(m, mode) {
			continue
		}
		// Honor the keep policy before maintenance: an error:/blocked: tool
		// result that KeepErrors would preserve must reach compact() verbatim.
		if a.keepPolicy&KeepErrors != 0 && isErrorMessage(m) {
			continue
		}
		idx = append(idx, i)
	}
	if len(idx) == 0 {
		return st, nil
	}
	if a.archiveDir != "" {
		originals := make([]provider.Message, 0, len(idx))
		for _, i := range idx {
			if mode == toolResultPrune && strings.HasPrefix(msgs[i].Content, snippedMarker) {
				continue
			}
			originals = append(originals, msgs[i])
		}
		if len(originals) > 0 {
			path, err := archiveMessages(a.archiveDir, originals)
			if err != nil {
				return st, fmt.Errorf("archive: %w", err)
			}
			st.Archive = path
		}
	}
	next := append([]provider.Message(nil), msgs...)
	for _, i := range idx {
		m := next[i]
		replacement := rewriteToolResult(m, mode, st.Archive)
		if replacement == m.Content {
			continue
		}
		st.SavedChars += len(m.Content) - len(replacement)
		m.Content = replacement
		next[i] = m
		st.Results++
	}
	if st.Results == 0 {
		return st, nil
	}
	a.session.Replace(next)
	a.session.IncrementRewrite()
	return st, nil
}

func shouldMaintainToolResult(m provider.Message, mode toolResultMaintenanceMode) bool {
	if m.Role != provider.RoleTool {
		return false
	}
	if strings.HasPrefix(m.Content, prunedMarker) {
		return false
	}
	if mode == toolResultSnip {
		return len(m.Content) >= minPruneBytes && !strings.HasPrefix(m.Content, snippedMarker)
	}
	if strings.HasPrefix(m.Content, snippedMarker) {
		return true
	}
	return len(m.Content) >= minPruneBytes
}

func rewriteToolResult(m provider.Message, mode toolResultMaintenanceMode, archive string) string {
	if mode == toolResultPrune {
		return pruneToolResult(m, archive)
	}
	return snipToolResult(m, archive)
}

func pruneToolResult(m provider.Message, archive string) string {
	if prior := originalToolArchive(m.Content); prior != "" {
		archive = prior
	}
	if archive == "" {
		archive = "not archived"
	}
	return fmt.Sprintf("%s%s, %d bytes archived to %s; re-run the tool if the data is needed again]", prunedMarker, m.Name, originalToolBytes(m.Content), archive)
}

func snipToolResult(m provider.Message, archive string) string {
	if archive == "" {
		archive = "not archived"
	}
	strategy := snipStrategyFor(m.Name)
	lines := strings.Split(m.Content, "\n")
	if len(lines) <= strategy.head+strategy.tail {
		headChars := minInt(strategy.headChars, len(m.Content)/2)
		tailChars := minInt(strategy.tailChars, len(m.Content)/4)
		return fmt.Sprintf("%s%s, %d bytes archived to %s; single large line truncated]\n%s\n[... %d bytes omitted ...]\n%s",
			snippedMarker, m.Name, len(m.Content), archive,
			firstRunes(m.Content, headChars),
			omittedBytes(m.Content, headChars, tailChars),
			lastRunes(m.Content, tailChars))
	}
	head := strings.Join(lines[:strategy.head], "\n")
	tail := strings.Join(lines[len(lines)-strategy.tail:], "\n")
	return fmt.Sprintf("%s%s, %d bytes archived to %s; showing first %d lines and last %d lines]\n%s\n[... %d lines omitted ...]\n%s",
		snippedMarker, m.Name, len(m.Content), archive, strategy.head, strategy.tail,
		head, len(lines)-strategy.head-strategy.tail, tail)
}

type snipStrategy struct {
	head      int
	tail      int
	headChars int
	tailChars int
}

func snipStrategyFor(name string) snipStrategy {
	switch {
	case name == "read_file" || name == "web_fetch":
		return snipStrategy{head: 120, tail: 12, headChars: 12000, tailChars: 2000}
	case name == "bash":
		return snipStrategy{head: 40, tail: 40, headChars: 8000, tailChars: 8000}
	case name == "grep" || name == "glob" || name == "ls":
		return snipStrategy{head: 80, tail: 8, headChars: 10000, tailChars: 1000}
	case strings.HasPrefix(name, "lsp_"):
		return snipStrategy{head: 60, tail: 10, headChars: 10000, tailChars: 1500}
	default:
		return snipStrategy{head: 60, tail: 12, headChars: 8000, tailChars: 2000}
	}
}

func originalToolBytes(content string) int {
	if strings.HasPrefix(content, snippedMarker) {
		end := strings.Index(content, " bytes archived to ")
		if end > len(snippedMarker) {
			fields := strings.Fields(content[len(snippedMarker):end])
			if len(fields) > 0 {
				var n int
				if _, err := fmt.Sscanf(fields[len(fields)-1], "%d", &n); err == nil && n > 0 {
					return n
				}
			}
		}
	}
	return len(content)
}

func originalToolArchive(content string) string {
	if !strings.HasPrefix(content, snippedMarker) {
		return ""
	}
	start := strings.Index(content, " bytes archived to ")
	if start < 0 {
		return ""
	}
	start += len(" bytes archived to ")
	end := strings.Index(content[start:], ";")
	if end < 0 {
		return ""
	}
	archive := strings.TrimSpace(content[start : start+end])
	if archive == "not archived" {
		return ""
	}
	return archive
}

func firstRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneBoundary(s, n) {
		n--
	}
	return s[:n]
}

func lastRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	start := len(s) - n
	for start < len(s) && !isRuneBoundary(s, start) {
		start++
	}
	return s[start:]
}

func omittedBytes(s string, head, tail int) int {
	omitted := len(s) - head - tail
	if omitted < 0 {
		return 0
	}
	return omitted
}

func isRuneBoundary(s string, i int) bool {
	return i == 0 || i == len(s) || (i > 0 && i < len(s) && (s[i]&0xc0) != 0x80)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
