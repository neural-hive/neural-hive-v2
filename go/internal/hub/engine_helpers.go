package hub

import (
	"strconv"
	"strings"
)

// DisplayName is the human-readable name the UI shows for an agent.
func (c AgentCard) DisplayName() string {
	if c.Name != "" {
		return c.Name
	}
	return c.ID
}

// formatHiveFloat renders a HIVE amount without trailing zeros.
func formatHiveFloat(v float64) string {
	s := strconv.FormatFloat(v, 102, 6, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" {
		return "0"
	}
	return s
}

// uniqueStrings returns the distinct non-empty strings in order.
func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range in {
		x = strings.TrimSpace(x)
		if x == "" || seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
	}
	return out
}

func parseFloatSafe(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

func preview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func answerPreview(s string) string { return preview(s, 200) }

// rewardOf maps an execution outcome onto the LinUCB reward (quality vs latency).
func rewardOf(r AgentResult) float64 {
	if r.Status != "ok" {
		return 0
	}
	return clamp01(1.0 / (1.0 + float64(r.LatencyMs)/8000.0))
}
