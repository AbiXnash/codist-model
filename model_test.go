package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func server(t *testing.T, h http.HandlerFunc) *Client {
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return New(s.URL+"/", "coder:7b", 4096)
}

func TestNewTrimsSlash(t *testing.T) {
	c := New("http://x:1/", "m", 1)
	if c.URL != "http://x:1" || c.HTTP == nil {
		t.Errorf("%+v", c)
	}
}

func TestStatus(t *testing.T) {
	cases := []struct {
		name, body string
		ok         bool
		want       string
	}{
		{"has the model", `{"models":[{"name":"other"},{"name":"coder:7b"}]}`, true, "ready"},
		{"has it as latest", `{"models":[{"name":"coder:7b:latest"}]}`, true, "ready"},
		{"missing", `{"models":[{"name":"other"}]}`, false, "ollama pull coder:7b"},
		{"not Ollama", `<html>`, false, "did not answer like Ollama"},
	}
	for _, c := range cases {
		cl := server(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/tags" {
				t.Errorf("asked %s", r.URL.Path)
			}
			io.WriteString(w, c.body)
		})
		ok, note := cl.Status(context.Background())
		if ok != c.ok || !strings.Contains(note, c.want) {
			t.Errorf("%s: %v %q", c.name, ok, note)
		}
	}
	ok, note := New("http://127.0.0.1:1", "m", 1).Status(context.Background())
	if ok || !strings.Contains(note, "no model server") {
		t.Errorf("no server: %v %q", ok, note)
	}
}

func TestChatJSON(t *testing.T) {
	var got map[string]any
	cl := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/chat" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"message":{"content":"{\"answer\":42}"},"prompt_eval_count":100,"eval_count":7,"prompt_eval_duration":2000000000,"eval_duration":500000000,"load_duration":1000}`)
	})
	var out struct{ Answer int }
	schema := map[string]any{"type": "object"}
	u, err := cl.ChatJSON(context.Background(), "sys", "user", schema, &out)
	if err != nil || out.Answer != 42 || u.PromptTokens != 100 || u.ReplyTokens != 7 || u.Prefill != 2*time.Second || u.DecodeRate() != 14 {
		t.Fatalf("%v %+v %+v", err, out, u)
	}
	if got["model"] != "coder:7b" || got["stream"] != false || got["format"].(map[string]any)["type"] != "object" {
		t.Errorf("request: %v", got)
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["content"] != "sys" || msgs[1].(map[string]any)["role"] != "user" {
		t.Errorf("messages: %v", msgs)
	}
	if got["options"].(map[string]any)["num_ctx"] != float64(4096) {
		t.Errorf("options: %v", got["options"])
	}
	// Without a schema the server is asked for plain JSON.
	if _, err := cl.ChatJSON(context.Background(), "s", "u", nil, &out); err != nil || got["format"] != "json" {
		t.Errorf("no schema: %v, format %v", err, got["format"])
	}
}

func TestChatJSONErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"server error", 500, "model crashed", "model server said 500 Internal Server Error: model crashed"},
		{"not JSON", 200, "nope", "model server reply"},
		{"content not JSON", 200, `{"message":{"content":"I think it is fine"}}`, "did not return valid JSON: I think it is fine"},
	}
	for _, c := range cases {
		cl := server(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			io.WriteString(w, c.body)
		})
		var out any
		if _, err := cl.ChatJSON(context.Background(), "s", "u", nil, &out); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	var out any
	if _, err := New("http://127.0.0.1:1", "m", 1).ChatJSON(context.Background(), "s", "u", nil, &out); err == nil || !strings.HasPrefix(err.Error(), "model server:") {
		t.Errorf("no server: %v", err)
	}
	if _, err := New("http://bad host", "m", 1).ChatJSON(context.Background(), "s", "u", nil, &out); err == nil {
		t.Error("a bad address should fail")
	}
}

func TestDecodeRateUnknown(t *testing.T) {
	if (Usage{ReplyTokens: 5}).DecodeRate() != 0 {
		t.Error("no decode time, no rate")
	}
}

func TestObserverSeesEveryCall(t *testing.T) {
	var seen []Call
	Observer = func(c Call) { seen = append(seen, c) }
	defer func() { Observer = nil }()
	cl := server(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"message":{"content":"{\"answer\":1}"},"prompt_eval_count":9,"eval_count":3}`)
	})
	var out struct{ Answer int }
	ctx := WithLabel(context.Background(), Label{Kind: "review", Title: "Review A.java", SHA: "abc"})
	if _, err := cl.ChatJSON(ctx, "sys", "user text", nil, &out); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0].Done || !seen[1].Done || seen[0].ID != seen[1].ID {
		t.Fatalf("a start and an end for the same call: %+v", seen)
	}
	end := seen[1]
	if end.Label.Kind != "review" || end.User != "user text" || end.Reply != `{"answer":1}` || end.Usage.PromptTokens != 9 || end.Err != "" {
		t.Fatalf("end: %+v", end)
	}
	bad := server(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	seen = nil
	if _, err := bad.ChatJSON(context.Background(), "s", "u", nil, &out); err == nil || len(seen) != 2 || seen[1].Err == "" {
		t.Fatalf("a failed call reports its error: %v %+v", err, seen)
	}
}
