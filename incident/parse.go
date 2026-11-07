// Package incident reads production logs: it finds the Java errors in them, groups repeats, and
// links stack frames to the project's code.
package incident

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Frame is one "at ..." line of a stack trace.
type Frame struct {
	Class, Method, File string
	Line                int
	Raw                 string
}

// Error is one distinct error: an exception with its causes, seen Count times.
type Error struct {
	Type    string // the outermost exception class
	Message string
	Causes  []string // "Caused by" exceptions, outermost first
	Root    string   // the innermost cause, usually the real problem
	Frames  []Frame  // frames of the innermost cause, or of the error itself if it has none
	Outer   []Frame  // frames of the outermost exception, when a cause has frames of its own
	Count   int
	First   string // the log line where it first appeared
}

var (
	// An exception line: com.x.FooException: message, optionally after a log prefix.
	exceptionLine = regexp.MustCompile(`(?:^|\s|:\s)((?:[a-z_][\w]*\.)+[A-Z]\w*(?:Exception|Error|Throwable))(?::\s*(.*))?$`)
	causedBy      = regexp.MustCompile(`^\s*Caused by:\s*((?:[a-z_][\w]*\.)+[A-Z][\w$]*)(?::\s*(.*))?$`)
	frameLine     = regexp.MustCompile(`^\s*at\s+(?:[\w.-]+/+)?([\w$.]+)\.([\w$<>]+)\(([^:)]*)(?::(\d+))?\)`)
)

// Parse finds the errors in a log, most frequent first.
func Parse(logs string) []Error {
	lines := strings.Split(strings.ReplaceAll(logs, "\r\n", "\n"), "\n")
	var errs []*Error
	var cur *Error
	var frames []Frame
	flush := func() {
		if cur == nil {
			return
		}
		cur.Frames = frames
		errs = append(errs, cur)
		cur, frames = nil, nil
	}
	for _, l := range lines {
		if m := frameLine.FindStringSubmatch(l); m != nil && cur != nil {
			n, _ := strconv.Atoi(m[4])
			frames = append(frames, Frame{Class: m[1], Method: m[2], File: m[3], Line: n, Raw: strings.TrimSpace(l)})
			continue
		}
		if m := causedBy.FindStringSubmatch(l); m != nil && cur != nil {
			cur.Causes = append(cur.Causes, short(m[1])+message(m[2]))
			cur.Root = short(m[1]) + message(m[2])
			if cur.Outer == nil {
				cur.Outer = frames
			}
			frames = nil // the innermost cause's frames are where it went wrong
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(l), "...") || strings.TrimSpace(l) == "" && cur != nil && len(frames) > 0 {
			continue
		}
		if m := exceptionLine.FindStringSubmatch(strings.TrimRight(l, " ")); m != nil {
			flush()
			cur = &Error{Type: short(m[1]), Message: strings.TrimSpace(m[2]), Count: 1, First: strings.TrimSpace(l)}
			cur.Root = cur.Type + message(cur.Message)
		}
	}
	flush()
	return group(errs)
}

func short(class string) string {
	if i := strings.LastIndex(class, "."); i >= 0 {
		return class[i+1:]
	}
	return class
}

func message(m string) string {
	if m = strings.TrimSpace(m); m != "" {
		return ": " + m
	}
	return ""
}

var numbers = regexp.MustCompile(`\b\d+\b|[0-9a-f]{8}-[0-9a-f-]{27}`)

// group merges repeats of the same error. Numbers and ids in messages differ between repeats, so
// they are ignored when comparing.
func group(errs []*Error) []Error {
	byKey := map[string]*Error{}
	var order []string
	for _, e := range errs {
		top := ""
		if len(e.Frames) > 0 {
			top = e.Frames[0].Class + "." + e.Frames[0].Method
		}
		key := e.Type + "|" + numbers.ReplaceAllString(e.Root, "#") + "|" + top
		if g, ok := byKey[key]; ok {
			g.Count++
			continue
		}
		byKey[key] = e
		order = append(order, key)
	}
	out := make([]Error, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}
