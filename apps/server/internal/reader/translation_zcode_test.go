package reader

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeTranslationZcodeSession struct {
	calls, closes int
	fail          bool
}

func TestZcodeClientConsecutiveTurns(t *testing.T) {
	var wire, sent bytes.Buffer
	for turn := 1; turn <= 2; turn++ {
		base := (turn - 1) * 3
		for id := base + 1; id <= base+2; id++ {
			fmt.Fprintf(&wire, "{\"id\":%d,\"result\":{}}\n", id)
		}
		for i, typ := range []string{"turn.started", "model.streaming", "turn.completed"} {
			payload := []string{`{}`, fmt.Sprintf(`{"kind":"text_delta","delta":"batch%d"}`, turn), fmt.Sprintf(`{"resultType":"success","response":"batch%d"}`, turn)}[i]
			fmt.Fprintf(&wire, `{"method":"session/event","params":{"sessionId":"s","turnId":"t%d","seq":%d,"type":"%s","payload":%s}}`+"\n", turn, base+i+1, typ, payload)
		}
		fmt.Fprintf(&wire, "{\"id\":%d,\"result\":{}}\n", base+3)
		// A replay from the previous turn can arrive before the next RPC reply.
		fmt.Fprintln(&wire, `{"method":"session/event","params":{"sessionId":"s","turnId":"t1","seq":2,"type":"model.streaming","payload":{"kind":"text_delta","delta":"stale"}}}`)
	}
	var snapshot zcodeSnapshot
	if err := json.Unmarshal([]byte(`{"settings":{"model":{"current":{"providerId":"p","modelId":"m"},"available":[{"ref":{"providerId":"p","modelId":"m"},"label":"m"}]}}}`), &snapshot); err != nil {
		t.Fatal(err)
	}
	c := &zcodeClient{ctx: context.Background(), session: "s", scan: bufio.NewScanner(&wire), life: &rpcLifecycle{encoder: json.NewEncoder(&sent)}}
	for turn := 1; turn <= 2; turn++ {
		var visible strings.Builder
		result, err := c.generate(snapshot, "", AIInput{}, func(s string) { visible.WriteString(s) })
		want := fmt.Sprintf("batch%d", turn)
		if err != nil || result != want || visible.String() != want {
			t.Fatalf("turn %d: %q %q %v", turn, result, visible.String(), err)
		}
	}
}

func TestZcodeBlockedProcessHelper(t *testing.T) {
	if os.Getenv("READER_ZCODE_BLOCKED_HELPER") != "1" {
		return
	}
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
	} // Ignore stop as well: exercise the forced-close watchdog.
	os.Exit(0)
}

func TestReusableZcodeCancelUnblocksRead(t *testing.T) {
	lifetime, cancel := context.WithCancel(context.Background())
	cmd := exec.Command(os.Args[0], "-test.run=^TestZcodeBlockedProcessHelper$")
	cmd.Env = append(os.Environ(), "READER_ZCODE_BLOCKED_HELPER=1")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &zcodeClient{ctx: lifetime, cmd: cmd, session: "s", scan: bufio.NewScanner(out)}
	c.life = newRPCLifecycle(lifetime, cmd, in, out)
	c.life.setCancel(map[string]any{"id": "stop", "method": "session/stop"})
	var snapshot zcodeSnapshot
	if err = json.Unmarshal([]byte(`{"settings":{"model":{"available":[{"ref":{"providerId":"p","modelId":"m"}}]}}}`), &snapshot); err != nil {
		t.Fatal(err)
	}
	s := &reusableZcodeSession{client: c, snapshot: snapshot, cancel: cancel}
	defer s.close()
	ctx, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	start := time.Now()
	_, err = s.generate(ctx, AIInput{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("cancellation did not unblock read")
	}
}

func (s *fakeTranslationZcodeSession) generate(ctx context.Context, in AIInput, delta func(string)) (string, error) {
	s.calls++
	if s.fail {
		return "", errors.New("failed turn")
	}
	delta(in.Prompt)
	return in.Prompt, ctx.Err()
}
func (s *fakeTranslationZcodeSession) close() { s.closes++ }

func TestTranslationZcodeReuseAndRecovery(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root+"/ai-work", 0700); err != nil {
		t.Fatal(err)
	}
	var sessions []*fakeTranslationZcodeSession
	a := &translationZcodeAdapter{root: root, open: func(ctx context.Context, work, model string) (translationZcodeSession, error) {
		s := &fakeTranslationZcodeSession{}
		sessions = append(sessions, s)
		return s, nil
	}}
	defer a.close()
	run := func(model string, emit func(ProviderEvent) error) error {
		_, err := a.stream(context.Background(), cliAdapter{model: model}, GenerateRequest{Input: AIInput{Prompt: "translation"}}, emit)
		return err
	}
	for i := 0; i < 2; i++ {
		if err := run("m", nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(sessions) != 1 || sessions[0].calls != 2 {
		t.Fatal("successful batches did not reuse session")
	}
	sessions[0].fail = true
	if run("m", nil) == nil || sessions[0].closes != 1 {
		t.Fatal("failed session was retained")
	}
	if err := run("m", nil); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatal("failed session was not recreated")
	}
	if err := run("other", nil); err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 || sessions[1].closes != 1 {
		t.Fatal("model switch did not rebuild")
	}
	if run("other", func(ProviderEvent) error { return errors.New("save failed") }) == nil || sessions[2].closes != 1 {
		t.Fatal("emit failure retained session")
	}
	if err := run("other", nil); err != nil {
		t.Fatal(err)
	}
	work := a.work
	a.close()
	a.close()
	if sessions[3].closes != 1 {
		t.Fatal("close is not idempotent")
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Fatal("workspace was not removed")
	}
}

func TestTranslationZcodeCanceledBeforeStart(t *testing.T) {
	a := &translationZcodeAdapter{open: func(context.Context, string, string) (translationZcodeSession, error) {
		t.Fatal("started after cancellation")
		return nil, nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.stream(ctx, cliAdapter{}, GenerateRequest{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestTranslationZcodeWorkersAreIndependent(t *testing.T) {
	s, _, _ := translationFixture(t)
	config := AIConfig{Primary: "zcode", TranslationModels: map[string]string{"zcode": "selected"},
		TranslationEfforts: map[string]map[string]string{"zcode": {"selected": "low"}}}
	first, a := s.translationService(config)
	defer a.close()
	second, b := s.translationService(config)
	defer b.close()
	if a.zcode == b.zcode || first.connections["zcode"].Adapter != a.zcode || second.connections["zcode"].Adapter != b.zcode {
		t.Fatal("workers must own independent translation adapters")
	}
	if a.zcode.sessionModel != "" || a.zcode.model != "selected" || a.zcode.level != "low" || a.zcode.task != taskTranslation {
		t.Fatal("translation settings not retained")
	}
}

func TestTranslationZcodeLiveReuse(t *testing.T) {
	if os.Getenv("READER_TEST_ZCODE_REUSE_LIVE") != "1" {
		t.Skip("opt-in real ZCode calls")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "ai-work"), 0700); err != nil {
		t.Fatal(err)
	}
	a := &translationZcodeAdapter{root: root}
	defer a.close()
	model := zcodeModelID(zcodeModelRef{ProviderID: "zai-api", ModelID: "GLM-5.3"})
	var first *reusableZcodeSession
	for i, word := range []string{"apple", "banana"} {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		start := time.Now()
		var visible strings.Builder
		result, err := a.stream(ctx, cliAdapter{model: model}, GenerateRequest{Input: AIInput{Prompt: "Translate only this word into Chinese: " + word, Effort: "low"}}, func(e ProviderEvent) error { visible.WriteString(e.Text); return nil })
		cancel() // Successful batch context is released before the next batch.
		if err != nil || result.Text != visible.String() || !strings.Contains(result.Text, []string{"苹果", "香蕉"}[i]) {
			t.Fatalf("batch %d: %q %v", i, result.Text, err)
		}
		current := a.session.(*reusableZcodeSession)
		if i == 0 {
			first = current
		} else if current != first {
			t.Fatal("session not reused")
		}
		if current.client.ctx.Err() != nil {
			t.Fatal("batch cleanup canceled reusable process")
		}
		t.Logf("batch %d completed in %s on PID %d", i+1, time.Since(start), current.client.cmd.Process.Pid)
	}
}
