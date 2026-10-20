package reader

import (
	"context"
	"os"
	"path/filepath"
)

type translationZcodeSession interface {
	generate(context.Context, AIInput, func(string)) (string, error)
	close()
}

type reusableZcodeSession struct {
	client   *zcodeClient
	snapshot zcodeSnapshot
	model    string
	cancel   context.CancelFunc
}

func openTranslationZcode(ctx context.Context, work, model string) (translationZcodeSession, error) {
	// The worker owns this lifetime; an individual successful batch must not
	// cancel the process when its timeout context is released.
	lifetime, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(ctx, cancel)
	client, snapshot, err := startZcode(lifetime, work)
	stop()
	if err != nil {
		cancel()
		return nil, err
	}
	s := &reusableZcodeSession{client: client, snapshot: snapshot, model: model, cancel: cancel}
	if ctx.Err() != nil {
		s.close()
		return nil, ctx.Err()
	}
	return s, nil
}

func (s *reusableZcodeSession) generate(ctx context.Context, in AIInput, delta func(string)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Cancellation stops the native turn, then the existing lifecycle watchdog
	// closes pipes and kills the process if ZCode does not acknowledge it.
	stop := context.AfterFunc(ctx, s.cancel)
	defer stop()
	text, err := s.client.generate(s.snapshot, s.model, in, delta)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return text, err
}

func (s *reusableZcodeSession) close() {
	s.cancel()
	s.client.close()
}

type translationZcodeAdapter struct {
	configuredCLIAdapter
	root, work, sessionModel string
	session                  translationZcodeSession
	open                     func(context.Context, string, string) (translationZcodeSession, error)
}

func (a *translationZcodeAdapter) close() {
	if a.session != nil {
		a.session.close()
		a.session = nil
	}
	if a.work != "" {
		_ = os.RemoveAll(a.work)
		a.work = ""
	}
}

func (a *translationZcodeAdapter) Stream(ctx context.Context, req GenerateRequest, emit func(ProviderEvent) error) (GenerateResult, error) {
	return a.streamWith(ctx, req, emit, func(cli cliAdapter, req GenerateRequest, emit func(ProviderEvent) error) (GenerateResult, error) {
		return a.stream(ctx, cli, req, emit)
	})
}

func (a *translationZcodeAdapter) stream(ctx context.Context, cli cliAdapter, req GenerateRequest, emit func(ProviderEvent) error) (GenerateResult, error) {
	if err := ctx.Err(); err != nil {
		return GenerateResult{}, err
	}
	if a.session != nil && a.sessionModel != cli.model {
		a.close()
	}
	if a.session == nil {
		work, err := os.MkdirTemp(filepath.Join(a.root, "ai-work"), "translation-zcode-")
		if err != nil {
			return GenerateResult{}, generationError(ErrorConfiguration, "无法创建隔离的翻译工作目录")
		}
		a.work, a.sessionModel = work, cli.model
		open := a.open
		if open == nil {
			open = openTranslationZcode
		}
		a.session, err = open(ctx, work, cli.model)
		if err != nil {
			a.close()
			return GenerateResult{}, err
		}
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	var emitErr error
	text, err := a.session.generate(child, req.Input, func(part string) {
		if emitErr == nil && emit != nil {
			emitErr = emit(ProviderEvent{Text: part})
			if emitErr != nil {
				cancel()
			}
		}
	})
	if emitErr != nil {
		err = emitErr
	}
	if child.Err() != nil && err == nil {
		err = child.Err()
	}
	if err != nil {
		a.close()
		return GenerateResult{}, err
	}
	return GenerateResult{Text: text, FinishReason: "stop"}, nil
}
