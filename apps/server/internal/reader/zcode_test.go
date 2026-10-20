package reader

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestZCodeFrames(t *testing.T) {
	for _, input := range []string{"", "{broken\n", "\xff\n"} {
		c := zcodeClient{ctx: context.Background(), scan: bufio.NewScanner(strings.NewReader(input))}
		if _, err := c.read(); err == nil {
			t.Fatalf("accepted truncated/invalid frame %q", input)
		}
	}
	var output bytes.Buffer
	c := zcodeClient{ctx: context.Background(), scan: bufio.NewScanner(strings.NewReader("{\"id\":\"host-1\",\"method\":\"execute\",\"params\":{}}\n")), life: &rpcLifecycle{encoder: json.NewEncoder(&output)}}
	if _, err := c.read(); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID    string `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil || response.ID != "host-1" || response.Error.Code != -32601 {
		t.Fatalf("host operation was not denied: %s", output.String())
	}
}

func TestZCodeNativeStreamReplay(t *testing.T) {
	s := zcodeStream{session: "s"}
	visible := ""
	for _, e := range []zcodeEvent{
		{SessionID: "s", TurnID: "t", Type: "turn.started", Seq: 1},
		{SessionID: "s", TurnID: "other", Type: "model.streaming", Seq: 2, Payload: json.RawMessage(`{"kind":"text_delta","delta":"wrong"}`)},
		{SessionID: "s", TurnID: "t", Type: "model.streaming", Seq: 3, Payload: json.RawMessage(`{"kind":"reasoning_delta","delta":"private"}`)},
		{SessionID: "s", TurnID: "t", Type: "model.streaming", Seq: 4, Payload: json.RawMessage(`{"kind":"text_delta","delta":"ok"}`)},
		{SessionID: "s", TurnID: "t", Type: "model.streaming", Seq: 4, Payload: json.RawMessage(`{"kind":"text_delta","delta":"ok"}`)},
		{SessionID: "s", TurnID: "t", Type: "part.delta", Seq: 5, Payload: json.RawMessage(`{"field":"text","delta":"ok"}`)},
	} {
		if _, err := s.event(e, func(v string) { visible += v }); err != nil {
			t.Fatal(err)
		}
	}
	if visible != "ok" {
		t.Fatalf("replay/foreign turn leaked: %q", visible)
	}
}

func TestZCodeCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, session, kind, payload string
		fail, done                   bool
	}{
		{"success", "s", "turn.completed", `{"resultType":"success","response":"你好"}`, false, true},
		{"cancelled", "s", "turn.completed", `{"resultType":"cancelled","response":"你好"}`, true, false},
		{"mismatch", "s", "turn.completed", `{"resultType":"success","response":"other"}`, true, false},
		{"foreign", "other", "turn.completed", `{"resultType":"success","response":"你好"}`, false, false},
		{"failed", "s", "turn.failed", `{}`, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := zcodeStream{session: "s"}
			s.text.WriteString("你好")
			done, err := s.event(zcodeEvent{SessionID: tc.session, Type: tc.kind, Payload: json.RawMessage(tc.payload)}, func(string) {})
			if (err != nil) != tc.fail || done != tc.done {
				t.Fatalf("done=%v err=%v", done, err)
			}
		})
	}
}

// Explicit opt-in: these tests use the user's configured ZCode and paid model.
func TestZCodeLive(t *testing.T) {
	if os.Getenv("READER_TEST_ZCODE_LIVE") != "1" {
		t.Skip("requires configured ZCode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	models, err := discoverZcodeModels(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("no models")
	}
	visible := ""
	answer, err := invokeZcode(ctx, t.TempDir(), models[0].ID, AIInput{Prompt: translationPrompt(Document{Type: "epub", Title: "Test"}, layoutManifest{Pages: 1}, translationBatch{Paragraphs: []translationParagraph{{BlockID: "p1", SourceHash: "h1", Source: "The experiment measures model accuracy."}}}, ""), Effort: "low"}, func(s string) { visible += s })
	if err != nil || answer != visible || !strings.Contains(answer, "实验") {
		t.Fatalf("translation %q, err %v", answer, err)
	}
	t.Logf("translation: %s", answer)
	image, expected, err := visionProbe()
	if err != nil {
		t.Fatal(err)
	}
	visionModel := defaultTaskModel(taskVision, "zcode", models)
	if visionModel == "" {
		t.Fatal("no image model")
	}
	answer, err = invokeZcode(ctx, t.TempDir(), visionModel, AIInput{Prompt: visionPrompt, Image: image, Effort: "low"}, func(string) {})
	if err != nil || !matchesVisionProbe(answer, expected) {
		t.Fatalf("vision %q expected %q err %v", answer, expected, err)
	}
	t.Log("random image challenge passed")
	child, stop := context.WithCancel(context.Background())
	client, _, err := startZcode(child, t.TempDir())
	if err != nil {
		stop()
		t.Fatal(err)
	}
	started := time.Now()
	stop()
	for {
		if _, err = client.read(); err != nil {
			break
		}
	}
	client.close()
	if time.Since(started) > 3*time.Second {
		t.Fatal("cancellation was not bounded")
	}
	t.Log("cancellation passed")
}

func TestZCodeStreamOnlyText(t *testing.T) {
	s := zcodeStream{session: "s"}
	visible := ""
	for _, field := range []string{"reasoning", "input", "output", "text"} {
		payload, _ := json.Marshal(map[string]string{"field": field, "delta": field})
		if _, err := s.event(zcodeEvent{SessionID: "s", Type: "part.delta", Payload: payload}, func(v string) { visible += v }); err != nil {
			t.Fatal(err)
		}
	}
	if visible != "text" {
		t.Fatalf("leaked non-text content: %q", visible)
	}
}

func TestZCodeModelIdentity(t *testing.T) {
	ref := zcodeModelRef{ProviderID: "custom/provider", ModelID: "model/one"}
	id := zcodeModelID(ref)
	decoded, err := parseZcodeModelID(id)
	if err != nil || decoded != ref {
		t.Fatalf("roundtrip: %+v %v", decoded, err)
	}
	if _, err = parseZcodeModelID("invalid"); err == nil {
		t.Fatal("accepted ambiguous model")
	}
}

func TestZCodeVisionSelection(t *testing.T) {
	models := []AgentModel{{ID: "text", Name: "GLM-5.3", Default: true}, {ID: "vision", Name: "GLM-5.3-Flash"}}
	if got := defaultTaskModel(taskVision, "zcode", models); got != "vision" {
		t.Fatalf("vision default %q", got)
	}
	c := AIConfig{Models: map[string]string{"zcode": "text"}, VisionModels: map[string]string{"zcode": "vision"}}
	before := configPrint(c, "zcode")
	c.VisionModels["zcode"] = "other"
	if configPrint(c, "zcode") == before {
		t.Fatal("changing image model must invalidate capability checks")
	}
}

func TestZCodeDefaultReasoning(t *testing.T) {
	var snapshot zcodeSnapshot
	if err := json.Unmarshal([]byte(`{"settings":{"model":{"available":[{"ref":{"providerId":"p","modelId":"m"},"reasoning":{"defaultLevel":"max"}}]}}}`), &snapshot); err != nil {
		t.Fatal(err)
	}
	ref := zcodeModelRef{ProviderID: "p", ModelID: "m"}
	for _, tc := range []struct{ input, want string }{{"", "max"}, {"low", "low"}} {
		selection := snapshot.selection(ref, tc.input)
		options, ok := selection["options"].(map[string]string)
		if !ok || options["reasoningLevel"] != tc.want {
			t.Fatalf("selection: %#v", selection)
		}
	}
}
