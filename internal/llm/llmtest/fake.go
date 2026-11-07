// Package llmtest provides a scripted llm.Provider for other packages' tests.
package llmtest

import (
	"context"
	"errors"
	"sync"

	"arxivlens/internal/llm"
)

// ErrScriptExhausted is returned by a call made after every scripted step was used.
var ErrScriptExhausted = errors.New("llmtest: no scripted step left")

// ErrEmptyStep is returned for a Step with no Resp, Err or Block.
var ErrEmptyStep = errors.New("llmtest: step has no Resp, Err or Block")

// Step is one scripted outcome. Block wins over Err, and Err over Resp.
type Step struct {
	Resp  *llm.Response
	Err   error
	Block bool // wait until ctx is done, then return nil, ctx.Err()
}

// Reply is a successful step with Text and Model "fake".
func Reply(text string) Step { return Step{Resp: &llm.Response{Text: text, Model: "fake"}} }

// Fail is a step that returns err.
func Fail(err error) Step { return Step{Err: err} }

// Hang is a step that blocks until ctx is done.
func Hang() Step { return Step{Block: true} }

// Fake returns its steps in order and records every request. Safe for concurrent use.
type Fake struct {
	mu    sync.Mutex
	steps []Step
	reqs  []llm.Request
}

var _ llm.Provider = (*Fake)(nil)

// New returns a Fake that plays steps in order.
func New(steps ...Step) *Fake { return &Fake{steps: append([]Step(nil), steps...)} }

func (f *Fake) Name() string { return "fake" }

// Complete records req, then plays the next step.
func (f *Fake) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, copyRequest(req))
	if len(f.steps) == 0 {
		f.mu.Unlock()
		return nil, ErrScriptExhausted
	}
	s := f.steps[0]
	f.steps = f.steps[1:]
	f.mu.Unlock()

	switch {
	case s.Block:
		<-ctx.Done()
		return nil, ctx.Err()
	case s.Err != nil:
		return nil, s.Err
	case s.Resp != nil:
		r := *s.Resp
		return &r, nil
	}
	return nil, ErrEmptyStep
}

// Requests returns deep copies of every request received, in call order.
func (f *Fake) Requests() []llm.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]llm.Request, len(f.reqs))
	for i, r := range f.reqs {
		out[i] = copyRequest(r)
	}
	return out
}

func copyRequest(r llm.Request) llm.Request {
	r.Messages = append([]llm.Message(nil), r.Messages...)
	return r
}
