// Package diff provides unified diff generation.
package diff

import (
	"fmt"
	"strings"
)

// UnifiedDiff generates a unified diff between two strings.
func UnifiedDiff(filename, oldContent, newContent string) string {
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")

	var hunks []hunk
	current := hunk{}

	// Simple diff: find changed regions
	maxLen := len(oldLines)
	if len(newLines) > maxLen {
		maxLen = len(newLines)
	}

	i, j := 0, 0
	for i < len(oldLines) || j < len(newLines) {
		if i < len(oldLines) && j < len(newLines) && oldLines[i] == newLines[j] {
			if current.hasChanges() {
				// Add context line after changes
				current.context = append(current.context, contextLine{lineNo: i + 1, text: oldLines[i], kind: ' '})
				if len(current.context) >= 3 {
					hunks = append(hunks, current)
					current = hunk{}
				}
			}
			i++
			j++
			continue
		}

		if !current.hasChanges() {
			// Add context before changes
			start := i - 3
			if start < 0 {
				start = 0
			}
			current.oldStart = start + 1
			current.newStart = j - (i - start) + 1
			if current.newStart < 1 {
				current.newStart = 1
			}
			for k := start; k < i; k++ {
				current.context = append(current.context, contextLine{lineNo: k + 1, text: oldLines[k], kind: ' '})
			}
		}

		// Find the next matching line
		oldEnd, newEnd := findNextMatch(oldLines, newLines, i, j)

		for k := i; k < oldEnd; k++ {
			current.context = append(current.context, contextLine{lineNo: k + 1, text: oldLines[k], kind: '-'})
		}
		for k := j; k < newEnd; k++ {
			current.context = append(current.context, contextLine{lineNo: k + 1, text: newLines[k], kind: '+'})
		}

		i = oldEnd
		j = newEnd
	}

	if current.hasChanges() {
		hunks = append(hunks, current)
	}

	if len(hunks) == 0 {
		return ""
	}

	// Format unified diff
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- a/%s\n", filename))
	sb.WriteString(fmt.Sprintf("+++ b/%s\n", filename))

	for _, h := range hunks {
		oldCount := 0
		newCount := 0
		for _, cl := range h.context {
			switch cl.kind {
			case '-':
				oldCount++
			case '+':
				newCount++
			case ' ':
				oldCount++
				newCount++
			}
		}

		sb.WriteString(fmt.Sprintf("@@ -%d,%d +%d,%d @@\n", h.oldStart, oldCount, h.newStart, newCount))
		for _, cl := range h.context {
			sb.WriteString(fmt.Sprintf("%c%s\n", cl.kind, cl.text))
		}
	}

	return sb.String()
}

type hunk struct {
	oldStart int
	newStart int
	context  []contextLine
}

type contextLine struct {
	lineNo int
	text   string
	kind   byte // ' ', '+', '-'
}

func (h *hunk) hasChanges() bool {
	for _, cl := range h.context {
		if cl.kind != ' ' {
			return true
		}
	}
	return false
}

func findNextMatch(oldLines, newLines []string, i, j int) (int, int) {
	// Look for the next line that matches in both
	for di := 0; di < 5 && i+di < len(oldLines); di++ {
		for dj := 0; dj < 5 && j+dj < len(newLines); dj++ {
			if di == 0 && dj == 0 {
				continue
			}
			if oldLines[i+di] == newLines[j+dj] {
				return i + di, j + dj
			}
		}
	}

	// No match found in window, consume one line from each
	oi := i + 1
	if oi > len(oldLines) {
		oi = len(oldLines)
	}
	oj := j + 1
	if oj > len(newLines) {
		oj = len(newLines)
	}
	return oi, oj
}

// CountChanges counts lines added and removed in a diff.
func CountChanges(diffStr string) (added, removed int) {
	for _, line := range strings.Split(diffStr, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	return
}
