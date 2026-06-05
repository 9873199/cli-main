// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package cmdpolicy

import (
	"github.com/larksuite/cli/extension/platform"
)

// suggestRisk returns the closest valid Risk literal by edit distance
// for risk_invalid diagnostics; input is never silently substituted.
// Case-insensitive ("WRITE" → "write"); empty in, empty out (the
// absent-annotation case goes to risk_not_annotated, not here).
func suggestRisk(bad string) string {
	if bad == "" {
		return ""
	}
	lowered := toLower(bad)
	candidates := []platform.Risk{
		platform.RiskRead, platform.RiskWrite, platform.RiskHighRiskWrite,
	}
	best := string(candidates[0])
	bestDist := levenshtein(lowered, best)
	for _, c := range candidates[1:] {
		if d := levenshtein(lowered, string(c)); d < bestDist {
			bestDist, best = d, string(c)
		}
	}
	return best
}

// toLower is an ASCII-only lowercase. Risk taxonomy values are
// ASCII; pulling in unicode here would be overkill.
func toLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// levenshtein computes the classic edit distance between two strings.
// O(len(a)*len(b)) time, O(min(a,b)) space. Three-element string set
// makes raw performance irrelevant — clarity beats trickiness here.
func levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min3(
				prev[j]+1,      // deletion
				curr[j-1]+1,    // insertion
				prev[j-1]+cost, // substitution
			)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
