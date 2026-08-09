// The requirement digest — the answer to "already delivered?" bound to the WORK a task demands,
// not to its identity (run id). A todo is not its id; it is what it asks for. Once a delivery of a
// todo has merged, the todo counts as delivered ONLY while its text still asks for the same work.
// Let its text GROW to demand more, and the added demand is not delivered — even though the id is
// the same and the earlier demand was met. This is the same class of bug this landscape has cleared
// before: a decision taken from a NAME instead of from the CONTENT the name carries.
//
// WHERE THE LINE SITS, AND WHY THERE.
//
// The determination must distinguish two kinds of edit to a todo's text:
//
//   - editorial (no new work): a typo fixed, a line rewrapped, spacing or case changed. The demand
//     is unchanged; a delivered todo stays delivered.
//   - substantive (new work): a requirement added, reworded into a different demand, or extended.
//     The delivered stand no longer covers what the todo asks for; it is delivered no more.
//
// A comparison that trips on every space is worthless — it would re-run a delivered todo because a
// line was rewrapped. A comparison that looks only at the id is today's bug — it never re-runs at
// all. So the line is drawn as: two requirement texts demand the SAME work iff, after normalising
// away pure presentation (case folded, every run of whitespace collapsed to one space, ends
// trimmed), they are either equal OR differ by no more than editorialEditBudget characters of edit
// distance. Normalisation alone answers "Umbruch" (a rewrap becomes identical); the small edit
// budget answers "Tippfehler" (a corrected letter or two).
//
// The budget is ABSOLUTE, not a fraction of the text, and deliberately so. The demand we must never
// miss — an added requirement — has a floor in size: the shortest real clause is still a dozen
// characters. The noise we must forgive — a typo — has a ceiling in size, and that ceiling does not
// grow just because the surrounding text is long. A proportional budget would do exactly the wrong
// thing: it would let a long todo hide a genuine new clause under the mass of its own text.
package runs

import (
	"fmt"
	"strings"
	"unicode"
)

// editorialEditBudget is the largest normalised edit distance still counted as an editorial change
// (a typo or two). Above it, the text has grown or been reworded into a different demand and the
// todo is no longer delivered. See the package doc for why this is an absolute count, not a ratio.
const editorialEditBudget = 8

// RequirementText is the substantive demand of a todo — its title and its task body, the words the
// user wrote to say what they want. It deliberately excludes the constitution and everything else
// the prompt is composed from: those reach every todo through the prompt and change on their own
// schedule, and a constitution edit must never re-open every todo ever delivered.
func RequirementText(title, task string) string {
	return title + "\n" + task
}

// RequirementDigest is RequirementText normalised for comparison: Unicode case folded, every run of
// whitespace collapsed to a single space, ends trimmed. This is the form recorded on a delivery as
// the stand that was delivered, and the form the current todo is measured against.
func RequirementDigest(title, task string) string {
	return normalizeRequirement(RequirementText(title, task))
}

func normalizeRequirement(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	started := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && started {
			b.WriteRune(' ')
		}
		b.WriteRune(unicode.ToLower(r))
		space = false
		started = true
	}
	return b.String()
}

// RequirementDemandsNewWork reports whether the current todo asks for work the delivered stand does
// not already cover — the ONE place the "already delivered" determination is bound to content. It
// returns the verdict and, when true, a human reason naming the stand the rejection would rest on.
//
// An empty deliveredDigest is NOT "changed": a delivery recorded before this determination existed
// (or issued by hand) captured no stand to compare against. We do not override a historical
// "delivered" verdict on missing evidence — doing so would treat every pre-existing delivery as
// suddenly undelivered and re-run the entire completed backlog. The digest is present on every
// delivery this determination itself records, so the gap closes forward, never backward.
func RequirementDemandsNewWork(deliveredDigest, currentTitle, currentTask string) (bool, string) {
	delivered := strings.TrimSpace(deliveredDigest)
	if delivered == "" {
		return false, ""
	}
	current := RequirementDigest(currentTitle, currentTask)
	if current == delivered {
		return false, ""
	}
	if editDistanceWithin(delivered, current, editorialEditBudget) {
		return false, ""
	}
	return true, fmt.Sprintf(
		"the todo text has grown or changed beyond an editorial edit since this stand was delivered — the delivered requirement was %q",
		ellipsis(delivered, 200))
}

// editDistanceWithin reports whether the Levenshtein distance between a and b is at most budget. It
// is capped: a length gap wider than the budget already forces a larger distance (each surplus
// character is at least one edit), and the row scan bails out the moment every cell exceeds the
// budget — so the common "far apart" answer costs O(len) and the close answer costs O(len·budget).
func editDistanceWithin(a, b string, budget int) bool {
	ra, rb := []rune(a), []rune(b)
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}
	if len(ra)-len(rb) > budget {
		return false
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := 0; j <= len(rb); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		rowMin := curr[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(prev[j]+1, curr[j-1]+1, prev[j-1]+cost)
			if curr[j] < rowMin {
				rowMin = curr[j]
			}
		}
		if rowMin > budget {
			return false
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)] <= budget
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

// ellipsis clips a digest for a one-line reason, marking that it was cut.
func ellipsis(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
