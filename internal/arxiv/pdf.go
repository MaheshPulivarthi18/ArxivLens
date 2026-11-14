package arxiv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// PDF download limits. A single overall timeout cannot tell a large PDF arriving slowly from a hung
// connection: arXiv served a 41 MB paper (2112.10752) at ~375 KB/s, ~110 s, so the old 45 s/60 s client
// timeouts failed it on every retry. Downloads are instead aborted when no bytes arrive for
// PDFStallTimeout, with PDFTotalTimeout as an outer bound and MaxPDFBytes as a size cap.
const (
	PDFStallTimeout = 60 * time.Second
	PDFTotalTimeout = 10 * time.Minute
	MaxPDFBytes     = 200 << 20
)

// PDFLimits parameterises FetchPDF; tests shrink them.
type PDFLimits struct {
	Stall, Total time.Duration
	MaxBytes     int64
}

// DefaultPDFLimits are the production limits.
var DefaultPDFLimits = PDFLimits{Stall: PDFStallTimeout, Total: PDFTotalTimeout, MaxBytes: MaxPDFBytes}

// ErrPDFStalled is returned (wrapped) when a PDF download receives no bytes for PDFLimits.Stall.
var ErrPDFStalled = errors.New("pdf download stalled")

// FetchPDF GETs pdfURL with userAgent and returns the body. It does not wait for a Limiter: callers do.
// client's own Timeout is ignored (its Transport is kept), because the limits above replace it.
// Non-200 responses return an error containing "status <code>".
func FetchPDF(ctx context.Context, client *http.Client, pdfURL, userAgent string, lim PDFLimits) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, lim.Total)
	defer cancel()

	var stalled atomic.Bool
	watchdog := time.AfterFunc(lim.Stall, func() { stalled.Store(true); cancel() })
	defer watchdog.Stop()
	wrap := func(what string, err error) error {
		if stalled.Load() {
			return fmt.Errorf("%s %s: %w: no data for %s", what, pdfURL, ErrPDFStalled, lim.Stall)
		}
		return fmt.Errorf("%s %s: %w", what, pdfURL, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pdfURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create pdf request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	c := *client
	c.Timeout = 0
	resp, err := c.Do(req)
	if err != nil {
		return nil, wrap("failed to download pdf from", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arxiv pdf download failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(&stallReader{r: io.LimitReader(resp.Body, lim.MaxBytes+1), t: watchdog, d: lim.Stall})
	if err != nil {
		return nil, wrap("failed to read pdf body from", err)
	}
	if int64(len(body)) > lim.MaxBytes {
		return nil, fmt.Errorf("pdf %s exceeds %d bytes", pdfURL, lim.MaxBytes)
	}
	return body, nil
}

// stallReader re-arms the stall watchdog every time bytes arrive.
type stallReader struct {
	r io.Reader
	t *time.Timer
	d time.Duration
}

func (s *stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.t.Reset(s.d)
	}
	return n, err
}
