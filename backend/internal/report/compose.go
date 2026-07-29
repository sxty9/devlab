package report

import (
	"fmt"
	"html"
	"strings"
	"time"

	"devlab/backend/internal/model"
)

// Item is one execution as it appears in a day's report — a summary, never a protocol. It carries the
// label, kind, affected repositories with the delivery stage each actually reached, and the day's
// consumption; transcripts, raw output and full error logs are deliberately absent (details live in
// the UI).
type Item struct {
	RunName   string
	TypeLabel string // "Automatic run" | "ToDo"
	StartedAt time.Time
	Finished  bool
	OK        bool
	Suspended bool // paused on the usage limit, awaiting resume
	Repos     []RepoLine
	InTokens  int
	OutTokens int
	CostUSD   float64
	LinkURL   string // optional per-execution deep link into the UI ("" ⇒ none)
}

// RepoLine names one affected repository and the delivery stage the run actually reached for it.
type RepoLine struct {
	Repo  string
	Stage string
}

// Content is the composed message in both plaintext and HTML.
type Content struct {
	Subject string
	Text    string
	HTML    string
}

// buckets splits a day's items into the three sections the user communication uses: what was
// completed, what is still running or pending, and what needs attention (failed or blocked). An
// unfinished execution (still running, or suspended on the usage limit) is pending; a finished-but-
// not-OK one needs attention; the rest completed.
func buckets(items []Item) (completed, pending, attention []Item) {
	for _, it := range items {
		switch {
		case !it.Finished:
			pending = append(pending, it)
		case it.OK:
			completed = append(completed, it)
		default:
			attention = append(attention, it)
		}
	}
	return
}

// Compose builds the day's report from its items and the day's rubrics (REQ-042.6). day is
// "YYYY-MM-DD". linkURL, when non-empty, is a link into the Mercury UI for the reader who needs
// details. items must be non-empty (the Reporter only composes for a day that had executions).
func Compose(day string, items []Item, rubrics []Rubric, linkURL string) Content {
	completed, pending, attention := buckets(items)

	subject := fmt.Sprintf("Mercury daily report — %s · %s", day, countPhrase(len(items)))
	if len(attention) > 0 {
		subject += fmt.Sprintf(" · %d need attention", len(attention))
	}
	if n := rubricLineCount(rubrics); n > 0 {
		subject += fmt.Sprintf(" · %s", alarmPhrase(n))
	}

	return Content{
		Subject: subject,
		Text:    composeText(day, items, completed, pending, attention, rubrics, linkURL),
		HTML:    composeHTML(day, items, completed, pending, attention, rubrics, linkURL),
	}
}

func composeText(day string, all, completed, pending, attention []Item, rubrics []Rubric, linkURL string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Mercury daily report for %s\n\n", day)
	b.WriteString("Mercury ran your automated jobs and concrete ToDos across the Holistic\n")
	b.WriteString("repositories. Here is what they did — successes and failures both.\n")

	textSection(&b, "COMPLETED", completed)
	textSection(&b, "IN PROGRESS / PENDING", pending)
	textSection(&b, "NEEDS ATTENTION", attention)
	for _, r := range rubrics {
		textRubric(&b, r)
	}

	in, out, cost, repos := totals(all)
	b.WriteString("\nTOTALS\n")
	fmt.Fprintf(&b, "  %s · %s · %s tokens · %s\n",
		countPhrase(len(all)), repoPhrase(repos), tokenPhrase(in, out), costPhrase(cost))

	if linkURL != "" {
		fmt.Fprintf(&b, "\nOpen Mercury for details: %s\n", linkURL)
	} else {
		b.WriteString("\nOpen the Mercury runs view for details.\n")
	}
	return b.String()
}

func textSection(b *strings.Builder, title string, items []Item) {
	fmt.Fprintf(b, "\n%s (%d)\n", title, len(items))
	if len(items) == 0 {
		b.WriteString("  —\n")
		return
	}
	for _, it := range items {
		fmt.Fprintf(b, "  • %s · %s\n", it.RunName, it.TypeLabel)
		if s := reposText(it); s != "" {
			fmt.Fprintf(b, "      %s\n", s)
		}
		if it.Suspended {
			b.WriteString("      paused on the usage limit — resumes automatically\n")
		}
		fmt.Fprintf(b, "      %s tokens · %s\n", tokenPhrase(it.InTokens, it.OutTokens), costPhrase(it.CostUSD))
	}
}

// textRubric renders one rubric: the finding, the repository it concerns, the next step, and —
// when the message repeated — how often over which period (REQ-032.5).
func textRubric(b *strings.Builder, r Rubric) {
	fmt.Fprintf(b, "\n%s (%d)\n", strings.ToUpper(r.Title), len(r.Lines))
	for _, l := range r.Lines {
		fmt.Fprintf(b, "  • %s\n", lineHead(l))
		if l.NextStep != "" {
			fmt.Fprintf(b, "      next: %s\n", l.NextStep)
		}
		if s := repeatPhrase(l); s != "" {
			fmt.Fprintf(b, "      %s\n", s)
		}
	}
}

// lineHead is "<repo>: <text>" — or just the text for a finding that concerns no single repo.
func lineHead(l RubricLine) string {
	if l.Repo == "" {
		return l.Text
	}
	return l.Repo + ": " + l.Text
}

// repeatPhrase states a bundled finding's count and period, and stays empty for a single
// occurrence (nothing to bundle, nothing to say).
func repeatPhrase(l RubricLine) string {
	if l.Count <= 1 {
		return ""
	}
	return fmt.Sprintf("%d times between %s and %s",
		l.Count, l.FirstAt.UTC().Format(time.RFC3339), l.LastAt.UTC().Format(time.RFC3339))
}

func rubricLineCount(rubrics []Rubric) int {
	n := 0
	for _, r := range rubrics {
		n += len(r.Lines)
	}
	return n
}

func alarmPhrase(n int) string {
	if n == 1 {
		return "1 alarm"
	}
	return fmt.Sprintf("%d alarms", n)
}

func reposText(it Item) string {
	if len(it.Repos) == 0 {
		return ""
	}
	parts := make([]string, 0, len(it.Repos))
	for _, r := range it.Repos {
		parts = append(parts, fmt.Sprintf("%s → %s", r.Repo, r.Stage))
	}
	return strings.Join(parts, ", ")
}

func composeHTML(day string, all, completed, pending, attention []Item, rubrics []Rubric, linkURL string) string {
	var b strings.Builder
	b.WriteString(`<div style="font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#1a1a1a;line-height:1.5">`)
	fmt.Fprintf(&b, `<h1 style="font-size:18px;margin:0 0 4px">Mercury daily report — %s</h1>`, html.EscapeString(day))
	b.WriteString(`<p style="margin:0 0 16px;color:#555">Mercury ran your automated jobs and concrete ToDos across the Holistic repositories. Here is what they did — successes and failures both.</p>`)

	htmlSection(&b, "Completed", "#137333", completed)
	htmlSection(&b, "In progress / pending", "#8a6d00", pending)
	htmlSection(&b, "Needs attention", "#b00020", attention)
	for _, r := range rubrics {
		htmlRubric(&b, r)
	}

	in, out, cost, repos := totals(all)
	b.WriteString(`<h2 style="font-size:14px;margin:20px 0 6px">Totals</h2>`)
	fmt.Fprintf(&b, `<p style="margin:0;color:#333">%s · %s · %s tokens · %s</p>`,
		html.EscapeString(countPhrase(len(all))), html.EscapeString(repoPhrase(repos)),
		html.EscapeString(tokenPhrase(in, out)), html.EscapeString(costPhrase(cost)))

	if linkURL != "" {
		fmt.Fprintf(&b, `<p style="margin:16px 0 0"><a href="%s">Open Mercury for details →</a></p>`, html.EscapeString(linkURL))
	} else {
		b.WriteString(`<p style="margin:16px 0 0;color:#555">Open the Mercury runs view for details.</p>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func htmlSection(b *strings.Builder, title, color string, items []Item) {
	fmt.Fprintf(b, `<h2 style="font-size:14px;margin:20px 0 6px;color:%s">%s (%d)</h2>`, color, html.EscapeString(title), len(items))
	if len(items) == 0 {
		b.WriteString(`<p style="margin:0 0 4px;color:#999">—</p>`)
		return
	}
	b.WriteString(`<ul style="margin:0;padding-left:18px">`)
	for _, it := range items {
		b.WriteString(`<li style="margin:0 0 8px">`)
		name := html.EscapeString(it.RunName)
		if it.LinkURL != "" {
			name = fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(it.LinkURL), name)
		}
		fmt.Fprintf(b, `<strong>%s</strong> <span style="color:#777">· %s</span>`, name, html.EscapeString(it.TypeLabel))
		if s := reposHTML(it); s != "" {
			fmt.Fprintf(b, `<br><span style="color:#333">%s</span>`, s)
		}
		if it.Suspended {
			b.WriteString(`<br><span style="color:#8a6d00">paused on the usage limit — resumes automatically</span>`)
		}
		fmt.Fprintf(b, `<br><span style="color:#777">%s tokens · %s</span>`,
			html.EscapeString(tokenPhrase(it.InTokens, it.OutTokens)), html.EscapeString(costPhrase(it.CostUSD)))
		b.WriteString(`</li>`)
	}
	b.WriteString(`</ul>`)
}

// htmlRubric renders one rubric as its own titled list — same findings, same wording as the text
// part (one composition, two renderings).
func htmlRubric(b *strings.Builder, r Rubric) {
	fmt.Fprintf(b, `<h2 style="font-size:14px;margin:20px 0 6px;color:#b00020">%s (%d)</h2>`,
		html.EscapeString(r.Title), len(r.Lines))
	b.WriteString(`<ul style="margin:0;padding-left:18px">`)
	for _, l := range r.Lines {
		b.WriteString(`<li style="margin:0 0 8px">`)
		fmt.Fprintf(b, `<span style="color:#333">%s</span>`, html.EscapeString(lineHead(l)))
		if l.NextStep != "" {
			fmt.Fprintf(b, `<br><span style="color:#555">next: %s</span>`, html.EscapeString(l.NextStep))
		}
		if s := repeatPhrase(l); s != "" {
			fmt.Fprintf(b, `<br><span style="color:#777">%s</span>`, html.EscapeString(s))
		}
		b.WriteString(`</li>`)
	}
	b.WriteString(`</ul>`)
}

func reposHTML(it Item) string {
	if len(it.Repos) == 0 {
		return ""
	}
	parts := make([]string, 0, len(it.Repos))
	for _, r := range it.Repos {
		parts = append(parts, fmt.Sprintf("%s → %s", html.EscapeString(r.Repo), html.EscapeString(r.Stage)))
	}
	return strings.Join(parts, ", ")
}

// totals sums consumption and counts the distinct repositories touched across the day.
func totals(items []Item) (in, out int, cost float64, repos int) {
	seen := map[string]bool{}
	for _, it := range items {
		in += it.InTokens
		out += it.OutTokens
		cost += it.CostUSD
		for _, r := range it.Repos {
			if !seen[r.Repo] {
				seen[r.Repo] = true
				repos++
			}
		}
	}
	return
}

// deliveryStage derives the delivery stage a run actually reached for one repository — read
// straight off the server stage array, honestly: a failure is named at the stage it failed,
// never glossed as progress; a blocked repo names its block.
func deliveryStage(rr model.RepoPipeline) string {
	if rr.Block != nil {
		return "blocked: " + rr.Block.Reason
	}
	lastExecuted := ""
	for _, sv := range rr.Stages {
		switch sv.State {
		case model.StepFailed:
			return "failed at " + stageWord(string(sv.Stage))
		case model.StepExecuted:
			lastExecuted = string(sv.Stage)
		case model.StepRunning:
			return "working on " + stageWord(string(sv.Stage))
		}
	}
	if lastExecuted != "" {
		return stageWord(lastExecuted)
	}
	return "not started"
}

// stageWord maps a pipeline step name to the noun for the stage it represents.
func stageWord(step string) string {
	switch step {
	case "preflight", "analyze":
		return "analyzed"
	case "implement":
		return "implemented"
	case "publish", "push":
		return "published"
	case "pull-request", "pr":
		return "PR opened"
	case "deliver-dev", "dev-deploy", "deploy":
		return "deployed"
	default:
		return step
	}
}

// typeLabel renders a run kind for humans.
func typeLabel(t model.RunKind) string {
	if t == model.KindTodo {
		return "ToDo"
	}
	return "Automatic run"
}

func countPhrase(n int) string {
	if n == 1 {
		return "1 execution"
	}
	return fmt.Sprintf("%d executions", n)
}

func repoPhrase(n int) string {
	if n == 1 {
		return "1 repository"
	}
	return fmt.Sprintf("%d repositories", n)
}

func tokenPhrase(in, out int) string {
	return fmt.Sprintf("%s in / %s out", commas(in), commas(out))
}

func costPhrase(cost float64) string {
	if cost <= 0 {
		return "$0.00"
	}
	if cost < 1 {
		return fmt.Sprintf("$%.4f", cost)
	}
	return fmt.Sprintf("$%.2f", cost)
}

// commas formats a non-negative int with thousands separators.
func commas(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	rem := len(s) % 3
	if rem > 0 {
		out = append(out, s[:rem]...)
	}
	for i := rem; i < len(s); i += 3 {
		if len(out) > 0 {
			out = append(out, ',')
		}
		out = append(out, s[i:i+3]...)
	}
	return string(out)
}
