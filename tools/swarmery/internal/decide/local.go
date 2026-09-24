package decide

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// LocalTimeout bounds one call to the local server. The classifier sits on the
// settle path of a finished run; a slow local model must cost seconds, not the
// run's remaining budget.
const LocalTimeout = 5 * time.Second

// Local is the OpenAI-compatible backend (LM Studio, llama.cpp server, vLLM,
// Ollama's /v1). Output is constrained by a JSON schema whose only property is
// an enum of the question's options; confidence comes from top_logprobs on the
// answer's first token when the server returns them, otherwise the answer is
// marked uncalibrated.
type Local struct {
	URL     string
	Model   string
	Client  *http.Client  // nil ⇒ http.DefaultClient
	Timeout time.Duration // 0 ⇒ LocalTimeout
}

func (l *Local) Name() string { return BackendLocal }

// endpoint turns SWARMERY_DECIDE_URL into the chat-completions URL: a full
// endpoint is used as is, a `/v1` base gets the path appended, a bare host gets
// both.
func (l *Local) endpoint() string {
	u := strings.TrimRight(strings.TrimSpace(l.URL), "/")
	switch {
	case strings.HasSuffix(u, "/chat/completions"):
		return u
	case strings.HasSuffix(u, "/v1"):
		return u + "/chat/completions"
	}
	return u + "/v1/chat/completions"
}

// systemPrompt is shared by every backend: the answer is ONE JSON object.
const systemPrompt = `You are a classifier. Read the evidence and answer the question with exactly one JSON object {"answer": "<option>"} where <option> is one of the listed options, copied verbatim. No prose.`

// userPrompt renders the question and its evidence.
func userPrompt(q Question) string {
	var b strings.Builder
	b.WriteString("Question: ")
	b.WriteString(q.Prompt)
	b.WriteString("\nOptions: ")
	b.WriteString(strings.Join(q.Options(), ", "))
	b.WriteString("\n\nEvidence:\n")
	b.WriteString(q.Input)
	return b.String()
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type topLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
}

type tokenLogprob struct {
	Token       string       `json:"token"`
	Logprob     float64      `json:"logprob"`
	TopLogprobs []topLogprob `json:"top_logprobs"`
}

type chatResponse struct {
	Choices []struct {
		Message  chatMessage `json:"message"`
		Logprobs *struct {
			Content []tokenLogprob `json:"content"`
		} `json:"logprobs"`
	} `json:"choices"`
}

// schemaFor is the response_format for q: an object with one enum property.
func schemaFor(q Question) map[string]any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "decision",
			"strict": true,
			"schema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"answer": map[string]any{"type": "string", "enum": q.Options()},
				},
				"required":             []string{"answer"},
				"additionalProperties": false,
			},
		},
	}
}

func (l *Local) Ask(ctx context.Context, q Question) (Answer, error) {
	timeout := l.Timeout
	if timeout <= 0 {
		timeout = LocalTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body, err := json.Marshal(map[string]any{
		"model":           l.Model,
		"temperature":     0,
		"max_tokens":      64,
		"logprobs":        true,
		"top_logprobs":    5,
		"response_format": schemaFor(q),
		"messages": []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt(q)},
		},
	})
	if err != nil {
		return Answer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint(), bytes.NewReader(body))
	if err != nil {
		return Answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := l.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Answer{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Answer{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Answer{}, fmt.Errorf("local server: HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return Answer{}, fmt.Errorf("local server: bad response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return Answer{}, fmt.Errorf("local server: no choices")
	}
	content := cr.Choices[0].Message.Content
	value, err := parseAnswer(q, content)
	if err != nil {
		return Answer{}, err
	}
	a := Answer{Value: value}
	if lp := cr.Choices[0].Logprobs; lp != nil {
		a.Probs, a.Confidence, a.Calibrated = optionProbs(q, content, lp.Content, value)
	}
	return a, nil
}

// parseAnswer extracts {"answer": X} from a reply (tolerating prose around the
// object) and maps X onto the question's options.
func parseAnswer(q Question, content string) (string, error) {
	start, end := strings.IndexByte(content, '{'), strings.LastIndexByte(content, '}')
	if start < 0 || end <= start {
		// LM Studio's reasoning parser strips the braces off a Qwen3 thinking
		// model's structured reply, leaving `done` or `answer": "done`. The
		// grammar still constrained the value, so a bare option is accepted.
		if v, ok := q.canonical(bareValue(content)); ok {
			return v, nil
		}
		return "", fmt.Errorf("no JSON object in reply %q", truncate(content, 120))
	}
	var obj struct {
		Answer json.RawMessage `json:"answer"`
	}
	if err := json.Unmarshal([]byte(content[start:end+1]), &obj); err != nil {
		return "", fmt.Errorf("bad JSON in reply: %w", err)
	}
	var s string
	if json.Unmarshal(obj.Answer, &s) != nil {
		// A score model may emit a bare number.
		s = strings.TrimSpace(string(obj.Answer))
	}
	v, ok := q.canonical(s)
	if !ok {
		return "", fmt.Errorf("answer %q is not one of %v", s, q.Options())
	}
	return v, nil
}

// optionProbs estimates per-option probabilities from the logprobs of the
// token where the answer value begins. Each alternative in that position's
// top_logprobs is mapped to the option it is a prefix of (the chosen value
// first); its probability is added to that option.
//
// This reads the FIRST token of the value only — options that share a first
// token cannot be told apart, and their probability is credited to the chosen
// one. That is an upper bound on confidence, which is why active mode also
// requires a threshold well above chance.
func optionProbs(q Question, content string, toks []tokenLogprob, value string) (map[string]float64, float64, bool) {
	valueStart := answerValueOffset(content)
	if valueStart < 0 || len(toks) == 0 {
		return nil, 0, false
	}
	// The tokens' text reproduces the content; find the token covering valueStart.
	pos := 0
	for _, t := range toks {
		next := pos + len(t.Token)
		if valueStart >= pos && valueStart < next {
			lead := t.Token[:valueStart-pos]
			alts := t.TopLogprobs
			if len(alts) == 0 {
				alts = []topLogprob{{Token: t.Token, Logprob: t.Logprob}}
			}
			probs := map[string]float64{}
			for _, alt := range alts {
				frag := alt.Token
				if strings.HasPrefix(frag, lead) {
					frag = frag[len(lead):]
				} else {
					frag = strings.TrimLeft(frag, "\": ")
				}
				opt, ok := optionFor(q, frag, value)
				if !ok {
					continue
				}
				probs[opt] += math.Exp(alt.Logprob)
			}
			if len(probs) == 0 {
				return nil, 0, false
			}
			for k, v := range probs {
				probs[k] = math.Min(v, 1)
			}
			conf, ok := probs[value]
			return probs, conf, ok
		}
		pos = next
	}
	return nil, 0, false
}

// answerValueOffset is the byte offset of the first character of the answer
// value inside `{"answer": "value"}`, or -1.
func answerValueOffset(content string) int {
	i := strings.Index(content, `"answer"`)
	if i < 0 {
		// A brace-stripped reply (see parseAnswer): the value is the first
		// non-space, non-quote character after any `answer":` remnant.
		if strings.ContainsRune(content, '{') || bareValue(content) == "" {
			return -1
		}
		return strings.Index(content, bareValue(content))
	}
	j := i + len(`"answer"`)
	for j < len(content) && (content[j] == ' ' || content[j] == ':' || content[j] == '\n' || content[j] == '\t') {
		j++
	}
	if j < len(content) && content[j] == '"' {
		j++
	}
	if j >= len(content) {
		return -1
	}
	return j
}

// optionFor maps a value fragment to the option it begins, preferring the
// chosen value.
func optionFor(q Question, frag, value string) (string, bool) {
	// A token may run past the value into the closing quote/brace.
	f := strings.ToLower(strings.TrimRight(strings.TrimSpace(frag), "\"}, \n"))
	if f == "" {
		return "", false
	}
	if strings.HasPrefix(strings.ToLower(value), f) {
		return value, true
	}
	for _, o := range q.Options() {
		if strings.HasPrefix(strings.ToLower(o), f) {
			return o, true
		}
	}
	return "", false
}

// bareValue is the answer value of a reply whose JSON braces were stripped:
// `done`, `"done"` or `answer": "done` all yield `done`.
func bareValue(content string) string {
	v := strings.TrimSpace(content)
	if i := strings.Index(v, "answer"); i >= 0 {
		v = strings.TrimLeft(v[i+len("answer"):], "\": \t\n")
	}
	return strings.Trim(v, "\" \t\n")
}
