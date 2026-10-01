package hub

import (
	"fmt"
	"sort"
	"strings"
)

// ComplexityThreshold is the score at or above which a task is treated as complex. The signals are
// deterministic and explainable so the classification can always be shown in the execution trace.
const ComplexityThreshold = 3

var objectiveVerbs = []string{
	"analyze", "analyse", "compare", "evaluate", "identify", "propose", "design", "implement",
	"research", "assess", "investigate", "recommend", "optimize", "summarize", "summarise",
	"explain", "review", "audit", "examine", "develop", "build", "plan", "investigate",
}

var domainKeywords = map[string][]string{
	"security":     {"security", "risk", "threat", "vulnerab", "attack", "exploit", "audit", "privacy"},
	"architecture": {"architecture", "design", "system", "infrastructure", "scalab", "component", "topolog"},
	"blockchain":   {"blockchain", "smart contract", "on-chain", "consensus", "token", "defi", "avalanche", "ledger"},
	"ai":           {" ai ", "agent", "model", "llm", "intelligen", "neural", "reasoning"},
	"data":         {"data", "dataset", "analytics", "metric", "signal", "pipeline"},
	"finance":      {"finance", "financial", "market", "trading", "liquidit", "price", "economic"},
	"code":         {"code", "implement", "refactor", "api", "function", "program", "debug"},
}

type tagRule struct {
	tag string
	kws []string
}

var tagRules = []tagRule{
	{"security", []string{"security", "risk", "threat", "vulnerab", "attack", "exploit", "privacy"}},
	{"architecture", []string{"architecture", "design", "system", "component", "infrastructure", "topolog"}},
	{"blockchain", []string{"blockchain", "smart contract", "on-chain", "consensus", "ledger", "token", "defi"}},
	{"coding", []string{"code", "implement", "refactor", "api", "function", "program", "debug"}},
	{"data", []string{"data", "dataset", "analytics", "metric", "signal"}},
	{"finance", []string{"finance", "financial", "market", "trading", "liquidit", "price"}},
	{"research", []string{"research", "study", "survey", "background", "explore", "investigate"}},
	{"analysis", []string{"analy", "assess", "evaluate", "compare", "examine", "review", "identify"}},
	{"planning", []string{"plan", "strateg", "roadmap", "approach", "propose", "implementation"}},
	{"validation", []string{"validate", "verify", "check", "consistenc", "reliab", "test"}},
	{"summarization", []string{"summar", "overview", "summary", "explain"}},
	{"reasoning", []string{"why", "reason", "trade", "implication", "decision", "inference"}},
}

// Classify decides whether a task is simple or complex using several explainable signals rather
// than raw length: how many objective verbs it contains, how clause-rich it is, and how many
// distinct domains it spans.
func Classify(text string) Complexity {
	lower := " " + strings.ToLower(text) + " "
	signals := []string{}
	score := 0
	verbs := matchedVerbs(lower)
	if len(verbs) >= 2 {
		score += 2
		signals = append(signals, "multiple objectives: "+strings.Join(verbs, ", "))
	} else if len(verbs) == 1 {
		score += 1
		signals = append(signals, "single objective: "+verbs[0])
	}
	clauses := countAny(lower, ", and ", " and ", " then ", " also ", " as well as ", ";")
	if clauses >= 2 {
		score += 2
		signals = append(signals, "several clauses")
	} else if clauses == 1 {
		score += 1
	}
	domains := DetectDomains(lower)
	if len(domains) >= 2 {
		score += 2
		signals = append(signals, "multiple domains: "+strings.Join(domains, ", "))
	} else if len(domains) == 1 {
		score += 1
	}
	level := "simple"
	if score >= ComplexityThreshold {
		level = "complex"
	}
	return Complexity{Level: level, Score: score, Signals: signals, Domains: domains}
}

// DetectDomains returns the sorted list of domains whose keywords appear in the task.
func DetectDomains(lower string) []string {
	out := []string{}
	for domain, kws := range domainKeywords {
		for _, kw := range kws {
			if strings.Contains(lower, kw) {
				out = append(out, domain)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

func matchedVerbs(lower string) []string {
	out := []string{}
	for _, v := range objectiveVerbs {
		if strings.Contains(lower, v) {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func countAny(s string, subs ...string) int {
	n := 0
	for _, sub := range subs {
		n += strings.Count(s, sub)
	}
	return n
}

// InferTags maps a piece of task text onto the capability tags it implies.
func InferTags(text string) []string {
	lower := strings.ToLower(text)
	seen := map[string]bool{}
	out := []string{}
	for _, rule := range tagRules {
		for _, kw := range rule.kws {
			if strings.Contains(lower, kw) {
				if !seen[rule.tag] {
					seen[rule.tag] = true
					out = append(out, rule.tag)
				}
				break
			}
		}
	}
	if len(out) == 0 {
		out = []string{"analysis", "reasoning"}
	}
	sort.Strings(out)
	return out
}

var objectiveSeparators = []string{"; ", ", and ", " and ", " then ", " also ", " as well as ", ". ", ", "}

const objectiveSep = "\u0000"

// SplitObjectives breaks a task into its distinct objectives, keeping the original wording.
func SplitObjectives(task string) []string {
	s := task
	for _, sep := range objectiveSeparators {
		s = strings.ReplaceAll(s, sep, objectiveSep)
	}
	out := []string{}
	for _, p := range strings.Split(s, objectiveSep) {
		p = strings.Trim(strings.TrimSpace(p), ".,;")
		if len(p) >= 3 {
			out = append(out, p)
		}
	}
	return out
}

// Decompose turns a task into exactly hive subtasks, each with the capability tags it requires.
// It first uses the task own objectives, then expands with domain- and facet-driven subtasks so
// the requested number of agents always has meaningful work.
func Decompose(task string, hive int, domains []string) []Subtask {
	if hive < 1 {
		hive = 1
	}
	subs := []Subtask{}
	for _, obj := range SplitObjectives(task) {
		if len(subs) >= hive {
			break
		}
		subs = append(subs, Subtask{Description: capitalize(obj), Tags: InferTags(obj)})
	}
	pool := domainFacets(domains)
	i := 0
	for len(subs) < hive && i < len(pool)*3 {
		f := pool[i%len(pool)]
		subs = append(subs, Subtask{Description: f.label, Tags: f.tags})
		i++
	}
	if len(subs) > hive {
		subs = subs[:hive]
	}
	for idx := range subs {
		subs[idx].ID = fmt.Sprintf("st-%d", idx+1)
		subs[idx].Description = strings.TrimSpace(subs[idx].Description)
	}
	return subs
}

type facet struct {
	label string
	tags  []string
}

func domainFacets(domains []string) []facet {
	out := []facet{}
	for _, d := range domains {
		switch d {
		case "security":
			out = append(out, facet{"Identify and analyse the security risks and their mitigations", []string{"security", "analysis"}})
		case "architecture":
			out = append(out, facet{"Design the architecture, components and their interactions", []string{"architecture", "planning"}})
		case "blockchain":
			out = append(out, facet{"Assess the blockchain and on-chain design considerations", []string{"blockchain", "analysis"}})
		case "ai":
			out = append(out, facet{"Analyse the AI and agent design and behaviour", []string{"reasoning", "analysis"}})
		case "data":
			out = append(out, facet{"Examine the data, metrics and evidence", []string{"data", "analysis"}})
		case "finance":
			out = append(out, facet{"Assess the financial and economic aspects", []string{"finance", "analysis"}})
		case "code":
			out = append(out, facet{"Review the implementation and code-level details", []string{"coding", "validation"}})
		}
	}
	out = append(out, facet{"Research the background and key facts", []string{"research", "analysis"}})
	out = append(out, facet{"Propose a concrete implementation strategy", []string{"planning", "architecture"}})
	out = append(out, facet{"Validate the findings and check for consistency", []string{"validation", "reasoning"}})
	out = append(out, facet{"Summarise the conclusions", []string{"summarization"}})
	return out
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
