// Package model talks to the model server the user runs. Codist does not contain a model: the user
// starts one (Ollama today) and gives Codist its address.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client calls one model on one server.
type Client struct {
	URL, Name string
	Ctx       int
	HTTP      *http.Client
}

// New makes a client. A review can take minutes on a laptop, so the timeout is long.
func New(url, name string, ctx int) *Client {
	return &Client{URL: strings.TrimRight(url, "/"), Name: name, Ctx: ctx, HTTP: &http.Client{Timeout: 15 * time.Minute}}
}

// Status says whether the server answers and has the model, in words a person can act on.
func (c *Client) Status(ctx context.Context) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logger().Debug("model server not reachable", "url", c.URL, "err", err)
		return false, "no model server at " + c.URL + "; start Ollama or set model.url in .codist/config.lua"
	}
	defer resp.Body.Close()
	var tags struct {
		Models []struct{ Name string } `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		logger().Warn("model server answered unlike Ollama", "url", c.URL, "status", resp.Status, "err", err)
		return false, "the server at " + c.URL + " did not answer like Ollama"
	}
	for _, m := range tags.Models {
		if m.Name == c.Name || m.Name == c.Name+":latest" {
			return true, "ready"
		}
	}
	logger().Debug("model not on server", "url", c.URL, "model", c.Name, "models", len(tags.Models))
	return false, c.Name + " is not on the server; run: ollama pull " + c.Name
}

// Usage is what one call cost, as the model server measured it: reading the prompt (prefill) sets the time to
// the first token, writing the reply (decode) the time per token after it.
type Usage struct {
	PromptTokens, ReplyTokens int
	Took                      time.Duration // the whole call, as Codist saw it
	Prefill, Decode, Load     time.Duration // as the server measured them; zero when it does not say
}

// DecodeRate is reply tokens per second, or 0 when unknown.
func (u Usage) DecodeRate() float64 {
	if u.Decode <= 0 {
		return 0
	}
	return float64(u.ReplyTokens) / u.Decode.Seconds()
}

// ChatJSON sends a system and user message and decodes the JSON reply into out. schema, when
// given, is a JSON schema the server must follow, so required fields such as a quoted line of code
// cannot be left out.
func (c *Client) ChatJSON(ctx context.Context, system, user string, schema any, out any) (Usage, error) {
	format := any("json")
	if schema != nil {
		format = schema
	}
	body, _ := json.Marshal(map[string]any{
		"model": c.Name, "stream": false, "format": format, "keep_alive": "30m",
		"options":  map[string]any{"temperature": 0.1, "num_ctx": c.Ctx, "num_predict": 1500},
		"messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
	})
	start := time.Now()
	call := Call{ID: nextID(), Label: LabelOf(ctx), Model: c.Name, System: system, User: user, Start: start}
	if Observer != nil {
		Observer(call)
	}
	var reply struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		PromptEvalCount    int   `json:"prompt_eval_count"`
		EvalCount          int   `json:"eval_count"`
		PromptEvalDuration int64 `json:"prompt_eval_duration"` // nanoseconds
		EvalDuration       int64 `json:"eval_duration"`
		LoadDuration       int64 `json:"load_duration"`
	}
	var u Usage
	var err error
	defer func() {
		if Observer != nil {
			call.Done, call.Reply, call.Usage = true, reply.Message.Content, u
			call.Usage.Took = time.Since(start)
			if err != nil {
				call.Err = err.Error()
			}
			Observer(call)
		}
	}()
	logger().Debug("model call", "model", c.Name, "system_chars", len(system), "user_chars", len(user), "schema", schema != nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		logger().Warn("model call failed", "model", c.Name, "took", time.Since(start), "err", err)
		err = fmt.Errorf("model server: %w", err)
		return Usage{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		logger().Warn("model reply cut short", "model", c.Name, "bytes", len(raw), "err", err)
	}
	if resp.StatusCode != http.StatusOK {
		logger().Warn("model server refused", "model", c.Name, "status", resp.Status, "body", fmt.Sprintf("%.300s", raw))
		err = fmt.Errorf("model server said %s: %s", resp.Status, strings.TrimSpace(string(raw)))
		return Usage{}, err
	}
	if err = json.Unmarshal(raw, &reply); err != nil {
		logger().Warn("model server reply unreadable", "model", c.Name, "err", err, "body", fmt.Sprintf("%.300s", raw))
		err = fmt.Errorf("model server reply: %w", err)
		return Usage{}, err
	}
	u = Usage{PromptTokens: reply.PromptEvalCount, ReplyTokens: reply.EvalCount, Took: time.Since(start),
		Prefill: time.Duration(reply.PromptEvalDuration), Decode: time.Duration(reply.EvalDuration), Load: time.Duration(reply.LoadDuration)}
	logger().Debug("model replied", "model", c.Name, "prompt_tokens", u.PromptTokens, "reply_tokens", u.ReplyTokens,
		"took", u.Took.Round(time.Millisecond), "prefill", u.Prefill.Round(time.Millisecond), "tok_per_s", int(u.DecodeRate()))
	if jerr := json.Unmarshal([]byte(reply.Message.Content), out); jerr != nil {
		logger().Warn("model reply not JSON", "model", c.Name, "reply", fmt.Sprintf("%.300s", reply.Message.Content))
		err = fmt.Errorf("the model did not return valid JSON: %.200s", reply.Message.Content)
		return u, err
	}
	return u, nil
}
