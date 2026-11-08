package incident

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	model "github.com/AbiXnash/codist-model"
)

// Code is what an analysis needs to know about the project, supplied by the caller so this package depends on
// neither the Java parser nor git.
type Code interface {
	// Class finds a project class by its name in a stack frame, without any $Inner part.
	Class(name string) (Class, bool)
	// Source reads a file of the project as it is now.
	Source(path string) ([]byte, bool)
	// LastChange is the commit that last changed one line of a file.
	LastChange(path string, line int) (Commit, bool)
	// History lists up to limit recent commits to a file, newest first.
	History(path string, limit int) []Commit
	// OpenIssues lists the titles of the issues still open in a file.
	OpenIssues(path string) []string
}

// Class is a project class a stack frame points into.
type Class struct{ Path, Name, NodeID string }

// Commit is a commit as an analysis shows it.
type Commit struct {
	SHA, Author, Subject string
	Time                 time.Time
}

// FrameView is one stack frame as the interface shows it.
type FrameView struct {
	Class  string `json:"class"`
	Method string `json:"method"`
	File   string `json:"file"`
	Line   int    `json:"line"`
}

// ErrorView is one distinct error.
type ErrorView struct {
	Exception string      `json:"exception"`
	Message   string      `json:"message"`
	Frames    []FrameView `json:"frames"`
	CausedBy  []string    `json:"causedBy,omitempty"`
	Count     int         `json:"count"`
}

// Blame is the commit that last changed a line.
type Blame struct {
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Time    int64  `json:"time"`
	Summary string `json:"summary"`
}

// Location is a place in the project's code an error went through.
type Location struct {
	Path    string   `json:"path"`
	Line    int      `json:"line"`
	Class   string   `json:"class"`
	Method  string   `json:"method"`
	NodeID  string   `json:"nodeId"`
	Snippet string   `json:"snippet"`
	Origin  bool     `json:"origin"`
	Hits    int      `json:"hits"`
	Blame   Blame    `json:"blame"`
	Issues  []string `json:"issues,omitempty"`
}

// Suspect is a commit that may have caused the incident, with why.
type Suspect struct {
	SHA     string   `json:"sha"`
	Author  string   `json:"author"`
	Time    int64    `json:"time"`
	Message string   `json:"message"`
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons"`
}

// Evidence is a line the model quoted, found in the logs or the code.
type Evidence struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
}

// Cause is a cause the model suggested, with the evidence that checked out.
type Cause struct {
	Title       string     `json:"title"`
	Explanation string     `json:"explanation"`
	Confidence  string     `json:"confidence"`
	Evidence    []Evidence `json:"evidence"`
	Fix         string     `json:"fix"`
	Path        string     `json:"path,omitempty"`
	Line        int        `json:"line,omitempty"`
}

// Report is the write-up a person can edit.
type Report struct {
	Summary    string `json:"summary"`
	Timeline   string `json:"timeline"`
	Impact     string `json:"impact"`
	RootCause  string `json:"rootCause"`
	Fix        string `json:"fix"`
	Prevention string `json:"prevention"`
}

// Analysis is an incident as the interface shows it. It is stored whole, so a page opens at once.
type Analysis struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	CreatedAt int64       `json:"createdAt"`
	Status    string      `json:"status"` // analysing, done, failed, confirmed
	Error     string      `json:"error,omitempty"`
	LogBytes  int         `json:"logBytes"`
	Excerpt   string      `json:"excerpt"`
	Errors    []ErrorView `json:"errors"`
	Locations []Location  `json:"locations"`
	Suspects  []Suspect   `json:"suspects"`
	Similar   []any       `json:"similar"`
	Causes    []Cause     `json:"causes"`
	Timeline  []string    `json:"timeline"`
	Report    Report      `json:"report"`
	Confirmed int         `json:"confirmed"`
	Runbook   string      `json:"runbook,omitempty"`
	Model     string      `json:"model"`
}

// Title names an incident: the title given, or the first error's root cause, or the time.
func Title(given, logs string, now time.Time) string {
	title := strings.TrimSpace(given)
	if title == "" {
		if errs := Parse(logs); len(errs) > 0 {
			title = errs[0].Root
		} else {
			title = "Incident " + now.Format("2 Jan 15:04")
		}
	}
	if len(title) > 120 {
		title = title[:119] + "…"
	}
	return title
}

// Read does everything that needs no model: the errors, their places in the code, the commits that last changed
// those lines, and recent commits to those files as suspects.
func Read(id, title string, created time.Time, logs string, code Code) Analysis {
	a := Analysis{ID: id, Title: title, CreatedAt: millis(created), Status: "analysing", LogBytes: len(logs),
		Errors: []ErrorView{}, Locations: []Location{}, Suspects: []Suspect{}, Similar: []any{}, Causes: []Cause{}, Timeline: []string{}}
	a.Excerpt = logs
	if len(a.Excerpt) > 4000 {
		a.Excerpt = a.Excerpt[:4000] + "\n…"
	}
	places := map[string]*Location{}
	var order []string
	for i, e := range Parse(logs) {
		ev := ErrorView{Exception: e.Type, Message: e.Message, Frames: []FrameView{}, CausedBy: e.Causes, Count: e.Count}
		for _, f := range e.Frames {
			ev.Frames = append(ev.Frames, FrameView{Class: f.Class, Method: f.Method, File: f.File, Line: f.Line})
		}
		// The innermost cause shows where it went wrong. When that is all library code, such as Hibernate, the
		// outer exception's frames still show where the project called it.
		frames := e.Frames
		if !inProject(code, frames) {
			frames = e.Outer
		}
		first := true
		for _, f := range frames {
			c, ok := code.Class(outerClass(f.Class))
			if !ok || f.Line <= 0 {
				continue
			}
			key := c.Path + ":" + strconv.Itoa(f.Line)
			if p, ok := places[key]; ok {
				p.Hits += e.Count
				continue
			}
			loc := &Location{Path: c.Path, Line: f.Line, Class: c.Name, Method: f.Method, NodeID: c.NodeID, Origin: first && i == 0,
				Hits: e.Count, Issues: code.OpenIssues(c.Path)}
			first = false
			if src, ok := code.Source(c.Path); ok {
				loc.Snippet = snippet(string(src), f.Line)
			}
			if b, ok := code.LastChange(c.Path, f.Line); ok {
				loc.Blame = Blame{SHA: b.SHA, Author: b.Author, Time: millis(b.Time), Summary: b.Subject}
			}
			places[key] = loc
			order = append(order, key)
		}
		a.Errors = append(a.Errors, ev)
	}
	for _, k := range order {
		a.Locations = append(a.Locations, *places[k])
	}
	a.Suspects = suspects(a.Locations, code)
	slog.Info("incident read", "lib", "model", "id", id, "log_bytes", len(logs), "errors", len(a.Errors), "locations", len(a.Locations), "suspects", len(a.Suspects))
	return a
}

// suspects ranks commits: those that last touched the failing lines first, then recent changes to those files.
func suspects(locations []Location, code Code) []Suspect {
	score := map[string]*Suspect{}
	add := func(sha, author, msg string, at time.Time, pts float64, why string) {
		s, ok := score[sha]
		if !ok {
			s = &Suspect{SHA: sha, Author: author, Time: millis(at), Message: msg, Reasons: []string{}}
			score[sha] = s
		}
		s.Score += pts
		s.Reasons = append(s.Reasons, why)
	}
	for _, l := range locations {
		if l.Blame.SHA != "" {
			pts := 2.0
			if l.Origin {
				pts = 4
			}
			add(l.Blame.SHA, l.Blame.Author, l.Blame.Summary, time.UnixMilli(l.Blame.Time), pts, fmt.Sprintf("last changed %s line %d", path.Base(l.Path), l.Line))
		}
		for i, c := range code.History(l.Path, 5) {
			add(c.SHA, c.Author, c.Subject, c.Time, 1/float64(i+2), "changed "+path.Base(l.Path))
		}
	}
	out := []Suspect{}
	for _, s := range score {
		out = append(out, *s)
	}
	sort.SliceStable(out, func(x, y int) bool {
		if out[x].Score != out[y].Score {
			return out[x].Score > out[y].Score
		}
		return out[x].SHA < out[y].SHA
	})
	return out[:min(8, len(out))]
}

func inProject(code Code, frames []Frame) bool {
	for _, f := range frames {
		if _, ok := code.Class(outerClass(f.Class)); ok && f.Line > 0 {
			return true
		}
	}
	return false
}

func outerClass(name string) string {
	c, _, _ := strings.Cut(name, "$")
	return c
}

// snippet is three lines either side of line, numbered.
func snippet(src string, line int) string {
	lines := strings.Split(src, "\n")
	var b strings.Builder
	for n := max(1, line-3); n <= min(len(lines), line+3); n++ {
		fmt.Fprintf(&b, "%d  %s\n", n, lines[n-1])
	}
	return b.String()
}

func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

var causeSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"summary":   map[string]any{"type": "string"},
		"impact":    map[string]any{"type": "string"},
		"rootCause": map[string]any{"type": "string"},
		"causes": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
			"title": map[string]any{"type": "string"}, "explanation": map[string]any{"type": "string"},
			"confidence": map[string]any{"type": "string", "enum": []string{"high", "medium", "low"}},
			"evidence":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"fix":        map[string]any{"type": "string"},
		}, "required": []string{"title", "explanation", "confidence", "evidence", "fix"}}},
	},
	"required": []string{"summary", "rootCause", "causes"},
}

const causeSystem = `You help an engineer find the cause of a production error in a Java service.
You get the errors, the project's code at each stack frame, and the commits that last changed those lines.
Base every statement on what you are given. In "evidence", copy exact lines from the logs or the code; each is checked.
Give one to three causes, most likely first. If the evidence does not show a cause, say so and use low confidence.
"fix" is a concrete change to the code. Reply with JSON only.`

// Explain asks the model for causes and checks every line it quotes against the logs and the code. With no model
// (nil), or no errors in the logs, it says so in the report. It always leaves the analysis done or failed.
func Explain(ctx context.Context, a *Analysis, logs string, m *model.Client) {
	if m == nil || len(a.Errors) == 0 {
		a.Status = "done"
		if len(a.Errors) == 0 {
			a.Report.Summary = "No Java errors were found in these logs. Codist looks for exception lines followed by \"at ...\" stack frames."
		} else {
			a.Report.Summary = "The model is not available, so only the errors, code locations and suspect commits are shown."
		}
		return
	}
	var b, corpus strings.Builder
	corpus.WriteString(logs)
	for _, e := range a.Errors[:min(3, len(a.Errors))] {
		fmt.Fprintf(&b, "## %s: %s (seen %d times)\n", e.Exception, e.Message, e.Count)
		for _, c := range e.CausedBy {
			fmt.Fprintf(&b, "Caused by: %s\n", c)
		}
		for _, f := range e.Frames[:min(12, len(e.Frames))] {
			fmt.Fprintf(&b, "  at %s.%s(%s:%d)\n", f.Class, f.Method, f.File, f.Line)
		}
	}
	for _, l := range a.Locations[:min(5, len(a.Locations))] {
		fmt.Fprintf(&b, "\n## %s line %d (%s.%s)\n```java\n%s```\n", l.Path, l.Line, l.Class, l.Method, l.Snippet)
		corpus.WriteString("\n" + l.Snippet)
		if l.Blame.SHA != "" {
			fmt.Fprintf(&b, "Line last changed by %s \"%s\" (%s)\n", l.Blame.SHA[:min(7, len(l.Blame.SHA))], l.Blame.Summary, l.Blame.Author)
		}
	}
	var out struct {
		Summary   string `json:"summary"`
		Impact    string `json:"impact"`
		RootCause string `json:"rootCause"`
		Causes    []struct {
			Title, Explanation, Confidence, Fix string
			Evidence                            []string
		} `json:"causes"`
	}
	a.Model = m.Name
	if _, err := m.ChatJSON(ctx, causeSystem, b.String(), causeSchema, &out); err != nil {
		slog.Warn("incident not explained", "lib", "model", "id", a.ID, "err", err)
		a.Status, a.Error = "failed", err.Error()
		return
	}
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	all, inLogs := squash(corpus.String()), squash(logs)
	for _, c := range out.Causes {
		cause := Cause{Title: c.Title, Explanation: c.Explanation, Confidence: c.Confidence, Fix: c.Fix, Evidence: []Evidence{}}
		dropped := 0
		for _, ev := range c.Evidence {
			if s := squash(ev); s != "" && strings.Contains(all, s) {
				kind := "log"
				if !strings.Contains(inLogs, s) {
					kind = "code"
				}
				cause.Evidence = append(cause.Evidence, Evidence{Kind: kind, Text: ev})
			} else {
				dropped++
			}
		}
		if dropped > 0 {
			slog.Info("model evidence dropped", "lib", "model", "id", a.ID, "cause", c.Title, "dropped", dropped, "kept", len(cause.Evidence))
		}
		if len(cause.Evidence) == 0 && len(c.Evidence) > 0 {
			cause.Confidence = "low"
			cause.Explanation += "\n\n(None of the model's quoted evidence is in the logs or the code, so treat this as a guess.)"
		} else if dropped > 0 {
			cause.Explanation += fmt.Sprintf("\n\n(%d quoted lines were not in the logs or code and were removed.)", dropped)
		}
		if len(a.Locations) > 0 {
			cause.Path, cause.Line = a.Locations[0].Path, a.Locations[0].Line
		}
		a.Causes = append(a.Causes, cause)
	}
	a.Report = Report{Summary: out.Summary, Impact: out.Impact, RootCause: out.RootCause}
	if len(a.Causes) > 0 {
		a.Report.Fix = a.Causes[0].Fix
	}
	a.Status = "done"
	slog.Info("incident explained", "lib", "model", "id", a.ID, "causes", len(a.Causes))
}

// Markdown is the incident as a report a person can paste into a ticket.
func Markdown(a Analysis) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", a.Title)
	if a.Report.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", a.Report.Summary)
	}
	if a.Report.RootCause != "" {
		fmt.Fprintf(&b, "## Root cause\n%s\n\n", a.Report.RootCause)
	}
	if len(a.Errors) > 0 {
		b.WriteString("## Errors\n")
		for _, e := range a.Errors {
			fmt.Fprintf(&b, "- %s: %s (%d times)\n", e.Exception, e.Message, e.Count)
		}
		b.WriteString("\n")
	}
	if len(a.Locations) > 0 {
		b.WriteString("## Where in the code\n")
		for _, l := range a.Locations {
			fmt.Fprintf(&b, "- %s:%d in %s.%s\n", l.Path, l.Line, l.Class, l.Method)
		}
		b.WriteString("\n")
	}
	if a.Report.Fix != "" {
		fmt.Fprintf(&b, "## Fix\n%s\n", a.Report.Fix)
	}
	return b.String()
}
