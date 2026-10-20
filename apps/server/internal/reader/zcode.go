package reader

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"
)

const zcodeSetupMessage = "ZCode 尚无可用模型，请在 ZCode 中配置 API Key 和模型；桌面免费额度不能直接用于此接口"

// Only inspect installation files. ZCode itself owns credentials and personal configuration.
func zcodeHome() (string, error) {
	if runtime.GOOS != "windows" {
		return "", errors.New("ZCode 接入目前仅支持 Windows")
	}
	candidates := []string{os.Getenv("READER_ZCODE_HOME")}
	if exe, err := exec.LookPath("ZCode.exe"); err == nil {
		candidates = append(candidates, filepath.Dir(exe))
	}
	candidates = append(candidates, filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "ZCode"))
	for drive := 'C'; drive <= 'Z'; drive++ {
		candidates = append(candidates, fmt.Sprintf("%c:\\Program Files\\ZCode", drive))
	}
	for _, home := range candidates {
		if home == "" {
			continue
		}
		valid := true
		for _, part := range []string{"ZCode.exe", "resources/glm/zcode.cjs", "resources/config/provider/zcode-builtin.json"} {
			st, err := os.Stat(filepath.Join(home, part))
			if err != nil || st.IsDir() {
				valid = false
				break
			}
		}
		if valid {
			return home, nil
		}
	}
	return "", errors.New("未找到 ZCode；自定义安装目录可设置 READER_ZCODE_HOME")
}

type zcodeModelRef struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
}

func zcodeModelID(ref zcodeModelRef) string {
	b, _ := json.Marshal(ref)
	return base64.RawURLEncoding.EncodeToString(b)
}
func parseZcodeModelID(id string) (zcodeModelRef, error) {
	var ref zcodeModelRef
	b, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return ref, err
	}
	err = json.Unmarshal(b, &ref)
	if err != nil || ref.ProviderID == "" || ref.ModelID == "" {
		return ref, errors.New("无效的 ZCode 模型")
	}
	return ref, nil
}

type zcodeSnapshot struct {
	Protocol struct {
		Name    string `json:"name"`
		Version int    `json:"version"`
	} `json:"protocol"`
	Session struct {
		ID        string `json:"id"`
		SessionID string `json:"sessionId"`
	} `json:"session"`
	Settings struct {
		Model struct {
			Current   zcodeModelRef `json:"current"`
			Available []struct {
				Ref             zcodeModelRef `json:"ref"`
				Label           string        `json:"label"`
				ProviderLabel   string        `json:"providerLabel"`
				DisabledReason  string        `json:"disabledReason"`
				ContextWindow   int           `json:"contextWindow"`
				MaxOutputTokens int           `json:"maxOutputTokens"`
				Reasoning       struct {
					Levels []struct {
						Value string `json:"value"`
					} `json:"levels"`
					DefaultLevel string `json:"defaultLevel"`
				} `json:"reasoning"`
			} `json:"available"`
		} `json:"model"`
	} `json:"settings"`
}

func (s zcodeSnapshot) models() []AgentModel {
	out := []AgentModel{}
	for _, m := range s.Settings.Model.Available {
		if m.DisabledReason != "" || m.Ref.ProviderID == "" || m.Ref.ModelID == "" {
			continue
		}
		model := AgentModel{ID: zcodeModelID(m.Ref), Name: m.Label, Description: m.ProviderLabel, Default: m.Ref == s.Settings.Model.Current, ContextWindow: m.ContextWindow, MaxOutputTokens: m.MaxOutputTokens, DefaultEffort: m.Reasoning.DefaultLevel}
		for _, level := range m.Reasoning.Levels {
			model.SupportedEfforts = append(model.SupportedEfforts, level.Value)
		}
		out = append(out, model)
	}
	return out
}

func (s zcodeSnapshot) selection(ref zcodeModelRef, effort string) map[string]any {
	if effort == "" {
		for _, m := range s.Settings.Model.Available {
			if m.Ref == ref {
				effort = m.Reasoning.DefaultLevel
				break
			}
		}
	}
	selection := map[string]any{"providerId": ref.ProviderID, "modelId": ref.ModelID}
	if effort != "" {
		selection["options"] = map[string]string{"reasoningLevel": effort}
	}
	return selection
}

type zcodeFrame struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}
type zcodeEvent struct {
	SessionID string          `json:"sessionId"`
	TurnID    string          `json:"turnId"`
	Type      string          `json:"type"`
	Seq       int64           `json:"seq"`
	Payload   json.RawMessage `json:"payload"`
}
type zcodeStream struct {
	session, turn, source string
	seq                   int64
	text                  strings.Builder
}

func (s *zcodeStream) event(e zcodeEvent, delta func(string)) (bool, error) {
	if e.SessionID != s.session {
		return false, nil
	}
	if e.Seq > 0 {
		if e.Seq <= s.seq {
			return false, nil
		}
		s.seq = e.Seq
	}
	if e.Type == "turn.started" {
		if s.turn != "" && s.turn != e.TurnID {
			return false, errors.New("ZCode 返回了多个回合")
		}
		s.turn = e.TurnID
	}
	if s.turn != "" && e.TurnID != "" && e.TurnID != s.turn {
		return false, nil
	}
	switch e.Type {
	case "part.delta", "model.streaming":
		var p struct {
			Field string `json:"field"`
			Kind  string `json:"kind"`
			Delta string `json:"delta"`
		}
		if json.Unmarshal(e.Payload, &p) != nil {
			return false, errors.New("无效的 ZCode 文本事件")
		}
		if e.Type == "part.delta" && p.Field != "text" || e.Type == "model.streaming" && p.Kind != "text_delta" {
			return false, nil
		}
		// Some versions publish both forms. Choose one incremental source per turn.
		if s.source == "" {
			s.source = e.Type
		}
		if s.source != e.Type {
			return false, nil
		}
		s.text.WriteString(p.Delta)
		if delta != nil {
			delta(p.Delta)
		}
	case "turn.failed":
		return false, errors.New("ZCode 调用失败，请在 ZCode 中检查模型和连接状态")
	case "turn.completed":
		var p struct {
			ResultType string  `json:"resultType"`
			Response   *string `json:"response"`
		}
		if json.Unmarshal(e.Payload, &p) != nil || p.ResultType != "success" || p.Response == nil || *p.Response != s.text.String() || strings.TrimSpace(s.text.String()) == "" {
			return false, errors.New("ZCode 未完整成功输出，已停止保存")
		}
		return true, nil
	}
	return false, nil
}

type zcodeClient struct {
	ctx     context.Context
	cmd     *exec.Cmd
	life    *rpcLifecycle
	scan    *bufio.Scanner
	session string
	stream  *zcodeStream
	delta   func(string)
	done    bool
	next    int
}

func startZcode(ctx context.Context, work string) (*zcodeClient, zcodeSnapshot, error) {
	var snapshot zcodeSnapshot
	home, err := zcodeHome()
	if err != nil {
		return nil, snapshot, err
	}
	cmd := exec.Command(filepath.Join(home, "ZCode.exe"), filepath.Join(home, "resources/glm/zcode.cjs"), "app-server", "--stdio")
	cmd.Dir = work
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "ELECTRON_RUN_AS_NODE=1", "ZCODE_BUILTIN_PROVIDER_CONFIG_FILE="+filepath.Join(home, "resources/config/provider/zcode-builtin.json"))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, snapshot, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, snapshot, err
	}
	if err = cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, snapshot, err
	}
	c := &zcodeClient{ctx: ctx, cmd: cmd, life: newRPCLifecycle(ctx, cmd, stdin, stdout), scan: bufio.NewScanner(stdout)}
	c.scan.Buffer(make([]byte, 4096), maxProviderFrame)
	raw, err := c.request("session/create", map[string]any{"workspace": map[string]string{"workspacePath": work, "workspaceKey": work}, "mode": "guarded", "persistence": "deferred", "toolAllowlist": []string{}, "mcpServers": []any{}, "titleGenerationEnabled": false, "offPeakToolEnabled": false, "dynamicWorkflowEnabled": false})
	if err == nil {
		err = json.Unmarshal(raw, &snapshot)
	}
	if err == nil && (snapshot.Protocol.Name != "ZCode Protocol" || snapshot.Protocol.Version != 1) {
		err = errors.New("不支持此 ZCode 后台协议版本")
	}
	c.session = snapshot.Session.SessionID
	if c.session == "" {
		c.session = snapshot.Session.ID
	}
	if err == nil && c.session == "" {
		err = errors.New("ZCode 未创建会话")
	}
	if err != nil {
		c.close()
		return nil, snapshot, err
	}
	c.life.setCancel(map[string]any{"id": "reader-stop", "method": "session/stop", "params": map[string]string{"sessionId": c.session}})
	return c, snapshot, nil
}
func (c *zcodeClient) close() { c.life.close(); _ = c.cmd.Wait() }
func (c *zcodeClient) read() (zcodeFrame, error) {
	var f zcodeFrame
	if !c.scan.Scan() {
		if c.ctx.Err() != nil {
			return f, c.ctx.Err()
		}
		return f, errors.New("ZCode 输出提前结束")
	}
	if !utf8.Valid(c.scan.Bytes()) || json.Unmarshal(c.scan.Bytes(), &f) != nil {
		return f, errors.New("ZCode 输出协议无效")
	}
	if f.Method != "" && len(f.ID) > 0 {
		if err := c.life.send(map[string]any{"id": f.ID, "error": map[string]any{"code": -32601, "message": "Reader exposes no host operations"}}); err != nil {
			return f, err
		}
	} else if f.Method == "session/event" && c.stream != nil && !c.done {
		var e zcodeEvent
		if err := json.Unmarshal(f.Params, &e); err != nil {
			return f, err
		}
		done, err := c.stream.event(e, c.delta)
		if err != nil {
			return f, err
		}
		c.done = c.done || done
	}
	return f, nil
}
func (c *zcodeClient) request(method string, params any) (json.RawMessage, error) {
	c.next++
	id := fmt.Sprint(c.next)
	if err := c.life.send(map[string]any{"id": c.next, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		f, err := c.read()
		if err != nil {
			return nil, err
		}
		if f.Method == "" && string(f.ID) == id {
			if len(f.Error) > 0 && string(f.Error) != "null" {
				return nil, errors.New("ZCode 拒绝请求：" + method)
			}
			return f.Result, nil
		}
	}
}
func discoverZcodeModels(ctx context.Context, work string) ([]AgentModel, error) {
	c, s, err := startZcode(ctx, work)
	if err != nil {
		return nil, err
	}
	defer c.close()
	models := s.models()
	if len(models) == 0 {
		return nil, errors.New(zcodeSetupMessage)
	}
	return models, nil
}
func invokeZcode(ctx context.Context, work, model string, in AIInput, delta func(string)) (string, error) {
	c, s, err := startZcode(ctx, work)
	if err != nil {
		return "", generationError(ErrorConfiguration, err.Error())
	}
	defer c.close()
	if len(s.models()) == 0 {
		return "", generationError(ErrorConfiguration, zcodeSetupMessage)
	}
	ref := s.Settings.Model.Current
	if model != "" {
		ref, err = parseZcodeModelID(model)
		if err != nil {
			return "", generationError(ErrorConfiguration, err.Error())
		}
	}
	if ref.ModelID == "" {
		ref = s.Settings.Model.Available[0].Ref
	}
	selection := s.selection(ref, in.Effort)
	if len(in.images()) > 0 && strings.EqualFold(ref.ModelID, "glm-5.3") {
		return "", generationError(ErrorConfiguration, "GLM-5.3 是文字模型，请将图片模型设置为 GLM-5.3-Flash")
	}
	attachments := []any{}
	for i, b := range in.images() {
		attachments = append(attachments, map[string]any{"kind": "image", "dataBase64": base64.StdEncoding.EncodeToString(b), "filename": fmt.Sprintf("image-%d.png", i), "mimeType": "image/png", "sizeBytes": len(b)})
	}
	c.stream = &zcodeStream{session: c.session}
	c.delta = delta
	// Apply the model explicitly before starting the turn.
	_, err = c.request("session/setModel", map[string]any{"sessionId": c.session, "model": selection})
	if err == nil {
		_, err = c.request("session/subscribe", map[string]any{"sessionId": c.session, "deliveryKind": "desktop-continuous", "includeSnapshot": false})
	}
	if err == nil {
		_, err = c.request("session/send", map[string]any{"sessionId": c.session, "content": "Reader instructions:\n" + readerSystemPrompt + "\n\nUser request:\n" + in.Prompt, "attachments": attachments})
	}
	for err == nil && !c.done {
		_, err = c.read()
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", generationError(ErrorProtocol, err.Error())
	}
	return c.stream.text.String(), nil
}

// ZCode's flagship may be text-only while its Flash model supports images.
type zcodeTaskAdapter struct{ text, vision configuredCLIAdapter }

func (a zcodeTaskAdapter) Stream(ctx context.Context, req GenerateRequest, emit func(ProviderEvent) error) (GenerateResult, error) {
	if len(req.Input.images()) > 0 {
		return a.vision.Stream(ctx, req, emit)
	}
	return a.text.Stream(ctx, req, emit)
}
