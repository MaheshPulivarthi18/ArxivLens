package llmtest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"arxivlens/internal/llm"
)

func ask(content string) llm.Request {
	return llm.Request{System: "sys", Messages: []llm.Message{{Role: llm.RoleUser, Content: content}}}
}

func TestFakeScript(t *testing.T) {
	boom := errors.New("boom")
	f := New(Reply("one"), Fail(boom), Step{Resp: &llm.Response{Text: "three", Model: "m", PromptTokens: 5}}, Step{})
	tests := []struct {
		wantText string
		wantErr  error
	}{
		{wantText: "one"},
		{wantErr: boom},
		{wantText: "three"},
		{wantErr: ErrEmptyStep},
		{wantErr: ErrScriptExhausted},
		{wantErr: ErrScriptExhausted},
	}
	for i, tt := range tests {
		resp, err := f.Complete(context.Background(), ask("q"))
		if tt.wantErr != nil {
			if !errors.Is(err, tt.wantErr) || resp != nil {
				t.Fatalf("call %d: want %v, got %v %v", i, tt.wantErr, resp, err)
			}
			continue
		}
		if err != nil || resp == nil || resp.Text != tt.wantText {
			t.Fatalf("call %d: want %q, got %v %v", i, tt.wantText, resp, err)
		}
	}
	if f.Name() != "fake" {
		t.Fatalf("Name() = %q", f.Name())
	}
	if n := len(f.Requests()); n != len(tests) {
		t.Fatalf("recorded %d requests, want %d", n, len(tests))
	}
}

func TestFakeReturnsCopies(t *testing.T) {
	step := Reply("x")
	f := New(step, step)
	r1, _ := f.Complete(context.Background(), ask("q"))
	r1.Text = "mutated"
	r2, _ := f.Complete(context.Background(), ask("q"))
	if r2 == nil || r2.Text != "x" || step.Resp.Text != "x" {
		t.Fatalf("a returned response aliases the script: %v", r2)
	}
}

func TestFakeRequests(t *testing.T) {
	f := New(Reply("a"), Reply("b"))
	req := ask("first")
	if _, err := f.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	req.Messages[0].Content = "caller mutated after the call"
	if _, err := f.Complete(context.Background(), ask("second")); err != nil {
		t.Fatal(err)
	}
	got := f.Requests()
	if len(got) != 2 || len(got[0].Messages) != 1 || len(got[1].Messages) != 1 {
		t.Fatalf("got %v", got)
	}
	if got[0].Messages[0].Content != "first" || got[1].Messages[0].Content != "second" || got[0].System != "sys" {
		t.Fatalf("requests not recorded as sent, in order: %q, %q", got[0].Messages[0].Content, got[1].Messages[0].Content)
	}
	got[0].Messages[0].Content = "reader mutated"
	if again := f.Requests(); len(again) != 2 || len(again[0].Messages) != 1 || again[0].Messages[0].Content != "first" {
		t.Fatal("Requests() must return deep copies")
	}
}

func TestFakeHang(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{name: "deadline", want: context.DeadlineExceeded,
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 50*time.Millisecond)
			}},
		{name: "cancel", want: context.Canceled, ctx: func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(50*time.Millisecond, cancel)
			return ctx, cancel
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := tt.ctx()
			defer cancel()
			f := New(Hang(), Reply("after"))
			start := time.Now()
			resp, err := f.Complete(ctx, ask("q"))
			if resp != nil || !errors.Is(err, tt.want) {
				t.Fatalf("want %v, got %v %v", tt.want, resp, err)
			}
			if el := time.Since(start); el < 40*time.Millisecond || el > 500*time.Millisecond {
				t.Fatalf("returned after %s; want about 50ms", el)
			}
			if r, err := f.Complete(context.Background(), ask("q")); err != nil || r.Text != "after" {
				t.Fatalf("next step: %v %v", r, err)
			}
		})
	}
}

func TestFakeConcurrent(t *testing.T) {
	const n = 50
	steps := make([]Step, n)
	for i := range steps {
		steps[i] = Reply("ok")
	}
	f := New(steps...)
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := f.Complete(context.Background(), ask("q")); err != nil {
				errs <- err
			}
			_ = f.Requests()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if got := len(f.Requests()); got != n {
		t.Fatalf("recorded %d requests, want %d", got, n)
	}
	if _, err := f.Complete(context.Background(), ask("q")); !errors.Is(err, ErrScriptExhausted) {
		t.Fatalf("want exhausted after %d calls, got %v", n, err)
	}
}
