package arxiv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Endpoints. Fetches go through APIURL / FetchURL so CI can point them at a local record/replay proxy
// (scripts/arxiv_replay.py) instead of live arXiv, which rate-limits shared CI runner IPs (HTTP 429).
// Links shown to users and stored source URLs stay canonical (PDFURL); only the fetch is redirected.
const (
	DefaultAPIURL = "http://export.arxiv.org/api/query"
	pdfPrefix     = "https://arxiv.org/pdf/"
)

// APIURL is the arXiv query API endpoint: $ARXIV_API_URL if set, else DefaultAPIURL.
func APIURL() string {
	if v := strings.TrimSpace(os.Getenv("ARXIV_API_URL")); v != "" {
		return v
	}
	return DefaultAPIURL
}

// FetchURL maps a canonical arXiv PDF URL to the URL to fetch it from: with $ARXIV_PDF_BASE set,
// "https://arxiv.org/pdf/<rest>" becomes "<base>/<rest>". Other URLs are returned unchanged.
func FetchURL(pdfURL string) string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("ARXIV_PDF_BASE")), "/")
	if base == "" || !strings.HasPrefix(pdfURL, pdfPrefix) {
		return pdfURL
	}
	return base + "/" + strings.TrimPrefix(pdfURL, pdfPrefix)
}

// StatusError is a non-200 arXiv response. Its text contains "status <code>".
type StatusError struct {
	Code       int
	RetryAfter time.Duration // from a Retry-After header in seconds; 0 when absent or unparsable
}

func (e *StatusError) Error() string { return fmt.Sprintf("arxiv returned status %d", e.Code) }

func newStatusError(resp *http.Response) *StatusError {
	e := &StatusError{Code: resp.StatusCode}
	if s, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	return e
}

// Retry is the retry policy for arXiv requests. The zero value makes a single attempt.
type Retry struct {
	Attempts int           // total attempts, including the first; < 1 means 1
	Base     time.Duration // wait before retry n (n >= 1) is Base * 3^(n-1), at least the server's Retry-After
	Max      time.Duration // cap on any single wait
	wait     func(ctx context.Context, d time.Duration) error
}

// DefaultRetry: 4 attempts, waiting 5 s, 15 s, 45 s (each at least Retry-After, at most 60 s).
var DefaultRetry = Retry{Attempts: 4, Base: 5 * time.Second, Max: 60 * time.Second}

// NoRetry makes a single attempt.
var NoRetry = Retry{Attempts: 1}

// Retryable reports whether err is worth retrying: HTTP 429 or 5xx, a network timeout, or a stalled PDF.
func Retryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusTooManyRequests || se.Code >= 500
	}
	var ne net.Error
	return errors.Is(err, ErrPDFStalled) || (errors.As(err, &ne) && ne.Timeout())
}

// Do calls attempt until it succeeds, fails with a non-retryable error, the attempts run out, or ctx ends.
// It returns attempt's last error.
func (r Retry) Do(ctx context.Context, attempt func() error) error {
	n := max(r.Attempts, 1)
	wait := r.wait
	if wait == nil {
		wait = sleepCtx
	}
	var err error
	for i := 1; ; i++ {
		if err = attempt(); err == nil || i >= n || !Retryable(err) || ctx.Err() != nil {
			return err
		}
		d := r.Base
		for j := 1; j < i; j++ {
			d *= 3
		}
		var se *StatusError
		if errors.As(err, &se) && se.RetryAfter > d {
			d = se.RetryAfter
		}
		if r.Max > 0 && d > r.Max {
			d = r.Max
		}
		if werr := wait(ctx, d); werr != nil {
			return err
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// GetAPI GETs an arXiv API URL and returns the body of a 200 response, retrying per r. Each attempt
// first waits for lim (nil: no wait) and sends userAgent. A non-200 response is a *StatusError, so an
// error page or empty body is never parsed as a feed.
func GetAPI(ctx context.Context, client *http.Client, lim *Limiter, reqURL, userAgent string, r Retry) ([]byte, error) {
	var body []byte
	err := r.Do(ctx, func() error {
		if lim != nil {
			if err := lim.Wait(ctx); err != nil {
				return err
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return fmt.Errorf("failed to create arxiv request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("arxiv API request failed: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			return newStatusError(resp)
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("failed to read arxiv response: %w", err)
		}
		body = b
		return nil
	})
	return body, err
}
