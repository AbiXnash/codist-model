package incident

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	model "github.com/AbiXnash/codist-model"
)

// shop is a fake project: OrderService.java, last changed by commit "c2" at line 31.
type shop struct{}

func (shop) Class(name string) (Class, bool) {
	if name == "com.shop.OrderService" {
		return Class{Path: "svc/src/main/java/com/shop/OrderService.java", Name: "OrderService", NodeID: name}, true
	}
	return Class{}, false
}
func (shop) Source(path string) ([]byte, bool) {
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		if i == 31 {
			b.WriteString("    total = total / count;\n")
		} else {
			b.WriteString("    // line\n")
		}
	}
	return []byte(b.String()), true
}
func (shop) LastChange(path string, line int) (Commit, bool) {
	return Commit{SHA: "c2c2c2c2c2", Author: "Dev", Subject: "Average totals", Time: time.UnixMilli(2000)}, line == 31
}
func (shop) History(path string, limit int) []Commit {
	return []Commit{{SHA: "c3", Subject: "Later"}, {SHA: "c2c2c2c2c2", Subject: "Average totals"}}
}
func (shop) OpenIssues(path string) []string { return []string{"Division by zero"} }

const logs = `java.lang.IllegalStateException: totals failed
	at com.shop.OrderService.total(OrderService.java:31)
	at com.shop.OrderService$Inner.run(OrderService.java:31)
Caused by: java.lang.ArithmeticException: / by zero
	at com.shop.OrderService.total(OrderService.java:31)
`

func TestTitle(t *testing.T) {
	now := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	if got := Title("", logs, now); got != "ArithmeticException: / by zero" {
		t.Errorf("from the root cause: %q", got)
	}
	if got := Title("", "quiet", now); got != "Incident 7 Nov 09:30" {
		t.Errorf("from the time: %q", got)
	}
	if got := Title(strings.Repeat("x", 200), "", now); len([]rune(got)) != 120 {
		t.Errorf("cut: %d", len([]rune(got)))
	}
}

func TestRead(t *testing.T) {
	a := Read("7", "T", time.UnixMilli(1000), logs, shop{})
	if a.Status != "analysing" || a.CreatedAt != 1000 || len(a.Errors) != 1 || a.Errors[0].Exception != "IllegalStateException" {
		t.Fatalf("%+v", a)
	}
	l := a.Locations
	if len(l) != 1 || l[0].Line != 31 || !l[0].Origin || l[0].Hits != 1 || l[0].Blame.SHA != "c2c2c2c2c2" ||
		!strings.Contains(l[0].Snippet, "31  ") || l[0].Issues[0] != "Division by zero" {
		t.Errorf("locations %+v", l)
	}
	if a.Suspects[0].SHA != "c2c2c2c2c2" || len(a.Suspects) != 2 || len(a.Suspects[0].Reasons) != 2 {
		t.Errorf("suspects %+v", a.Suspects)
	}
	big := Read("8", "T", time.Time{}, strings.Repeat("x", 5000), shop{})
	if len(big.Excerpt) != 4000+len("\n…") || big.CreatedAt != 0 || len(big.Locations) != 0 {
		t.Errorf("long logs: %d", len(big.Excerpt))
	}
}

func fakeModel(t *testing.T, status int, reply string) *model.Client {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		b, _ := json.Marshal(map[string]any{"message": map[string]string{"content": reply}})
		w.Write(b)
	}))
	t.Cleanup(s.Close)
	return model.New(s.URL, "fake", 4096)
}

func TestExplain(t *testing.T) {
	a := Read("7", "T", time.Now(), logs, shop{})
	m := fakeModel(t, 200, `{"summary":"S","impact":"I","rootCause":"R","causes":[
		{"title":"Divides by zero","explanation":"E","confidence":"high","fix":"F",
		 "evidence":["java.lang.ArithmeticException: / by zero","total = total / count;","not anywhere"]},
		{"title":"Guess","explanation":"G","confidence":"high","fix":"F2","evidence":["made up"]}]}`)
	Explain(context.Background(), &a, logs, m)
	c := a.Causes
	if a.Status != "done" || a.Model != "fake" || len(c) != 2 || a.Report.Fix != "F" || a.Report.RootCause != "R" {
		t.Fatalf("%+v", a)
	}
	if len(c[0].Evidence) != 2 || c[0].Evidence[0].Kind != "log" || c[0].Evidence[1].Kind != "code" ||
		!strings.Contains(c[0].Explanation, "1 quoted lines") || c[0].Line != 31 {
		t.Errorf("first cause %+v", c[0])
	}
	if c[1].Confidence != "low" || !strings.Contains(c[1].Explanation, "treat this as a guess") {
		t.Errorf("second cause %+v", c[1])
	}
	md := Markdown(a)
	for _, want := range []string{"# T", "## Root cause\nR", "## Errors", "## Where in the code", "## Fix\nF"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q", want)
		}
	}
}

func TestExplainWithoutModel(t *testing.T) {
	a := Read("7", "T", time.Now(), logs, shop{})
	Explain(context.Background(), &a, logs, nil)
	if a.Status != "done" || !strings.Contains(a.Report.Summary, "model is not available") {
		t.Errorf("%+v", a.Report)
	}
	quiet := Read("8", "T", time.Now(), "all fine", shop{})
	Explain(context.Background(), &quiet, "all fine", fakeModel(t, 200, "{}"))
	if !strings.Contains(quiet.Report.Summary, "No Java errors") {
		t.Errorf("%+v", quiet.Report)
	}
	failed := Read("9", "T", time.Now(), logs, shop{})
	Explain(context.Background(), &failed, logs, fakeModel(t, 500, "busy"))
	if failed.Status != "failed" || failed.Error == "" {
		t.Errorf("%+v", failed)
	}
}
