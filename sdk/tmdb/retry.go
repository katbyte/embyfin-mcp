package tmdb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// retryPauses are the waits before each retry of a TMDB read that failed in
// a way that passes: a connection reset or timed out, a 429, a 5xx. One
// "connection reset by peer" on one film's alternative titles used to be the
// end of TMDB for a whole sweep, and every row after it was judged without
// TMDB: two hundred false findings over a library of twenty thousand films.
var retryPauses = []time.Duration{250 * time.Millisecond, time.Second, 3 * time.Second}

// TriesPerRead is how many times one read is sent, at most.
func TriesPerRead() int { return len(retryPauses) + 1 }

const (
	// attemptTimeout is how long one try waits for TMDB to answer
	attemptTimeout = 15 * time.Second
	// clientTimeout bounds a read with every try and pause in it
	clientTimeout = 75 * time.Second
	// retryAfterMax caps how long a 429's Retry-After is waited for
	retryAfterMax = 10 * time.Second
)

// Retrying is a transport that tries a GET again when TMDB did not answer it
// (a reset, a try that timed out) or answered that it could not (a 429 or a
// 5xx), a few times with a growing wait, and hands back the last failure when
// none took. Every read the tools make of TMDB is a GET, which asking again
// cannot change; other methods are sent once. nil is the default transport.
func Retrying(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}

	return retrying{next: rt, pauses: retryPauses, attempt: attemptTimeout}
}

// HTTPClient is the client TMDB is read with: rt, tried again as Retrying
// does, each try given attemptTimeout and the whole read clientTimeout. A
// transport Guarded already made is used as it is.
func HTTPClient(rt http.RoundTripper) *http.Client {
	if _, guarded := rt.(*Breaker); !guarded {
		rt = Retrying(rt)
	}

	return &http.Client{Timeout: clientTimeout, Transport: rt}
}

// breakerAfter is how many reads in a row, each already tried again, fail
// before TMDB is taken to be down; breakerRest is how long it is then left
// alone before one read is let through to see whether it is back.
const (
	breakerAfter = 3
	breakerRest  = time.Minute
)

// Breaker is Retrying with a count around it: once breakerAfter reads in a
// row have failed through every retry, TMDB is taken to be down and each read
// after fails at once with a DownError, until breakerRest has passed and one
// read answers. Only a read TMDB answered resets the count - a 404 is an
// answer - so reads that never reach TMDB (a sweep's rows with nothing to
// ask, answers kept from before, a read the caller cancelled) neither reset
// nor trip it; a read that ran out of time is TMDB not answering, and trips
// it. A read that failed while TMDB answered another is that read's failure,
// not TMDB down, and counts neither way: with reads sent a few at a time, one
// failing through its retries finished after the answers sent beside it, and
// three such in a row took a TMDB that was answering to be down. Without it a
// TMDB that hangs cost a minute a read, over every row of a sweep: hours.
type Breaker struct {
	next http.RoundTripper
	now  func() time.Time // the clock, for a test to move; nil is the wall clock

	mu       sync.Mutex
	inARow   int
	openedAt time.Time
	last     error
	answers  int // reads TMDB has answered, to tell a failure beside an answer
}

// Guarded is rt tried again as Retrying does, behind a Breaker: one to share
// between every client of one sweep, so the reads of one kind and another
// count together.
func Guarded(rt http.RoundTripper) *Breaker {
	return &Breaker{next: Retrying(rt)}
}

// DownError is a read not made because TMDB was taken to be down.
type DownError struct {
	Failed int
	Last   error
}

func (e *DownError) Error() string {
	return fmt.Sprintf("TMDB not asked: taken to be down after %d reads in a row failed, the last with %v", e.Failed, e.Last)
}

func (b *Breaker) clock() time.Time {
	if b.now != nil {
		return b.now()
	}

	return time.Now()
}

func (b *Breaker) RoundTrip(req *http.Request) (*http.Response, error) {
	b.mu.Lock()
	if b.inARow >= breakerAfter && b.clock().Sub(b.openedAt) < breakerRest {
		err := &DownError{Failed: b.inARow, Last: b.last}
		b.mu.Unlock()

		return nil, err
	}
	answered := b.answers
	b.mu.Unlock()

	resp, err := b.next.RoundTrip(req)
	// a read the caller cancelled is none of TMDB's doing; one that ran out
	// of time - the client's own timeout, a slow 5xx waited out past it - is
	// TMDB not answering, and counts
	failed := err != nil && !errors.Is(req.Context().Err(), context.Canceled) ||
		err == nil && (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError)

	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case failed && b.answers > answered:
		// TMDB answered another read while this one failed: it is up
	case failed:
		b.inARow++
		b.last = err
		if err == nil {
			b.last = fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		if b.inARow >= breakerAfter {
			b.openedAt = b.clock()
		}
	case err == nil:
		b.inARow = 0
		b.answers++
	}

	return resp, err
}

type retrying struct {
	next    http.RoundTripper
	pauses  []time.Duration
	attempt time.Duration
}

func (r retrying) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return r.next.RoundTrip(req)
	}
	for try := 0; ; try++ {
		ctx, cancel := context.WithTimeout(req.Context(), r.attempt)
		resp, err := r.next.RoundTrip(req.WithContext(ctx))
		if try >= len(r.pauses) || !passing(req.Context(), resp, err) {
			// the try's time runs until its answer is read and closed
			if resp != nil {
				resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
			} else {
				cancel()
			}

			return resp, err
		}
		wait := r.pauses[try]
		if resp != nil {
			if after, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && after > 0 {
				wait = min(max(wait, time.Duration(after)*time.Second), retryAfterMax)
			}
			// the answer given up on is closed, or its connection is held
			if cerr := resp.Body.Close(); cerr != nil {
				cancel()

				return nil, cerr
			}
		}
		cancel()
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(wait):
		}
	}
}

// passing says whether a failure is one that asking again may get past: no
// answer at all (a reset, a dropped connection, a try that ran out of time),
// a 429, or a 5xx. A call the caller has given up on is not.
func passing(caller context.Context, resp *http.Response, err error) bool {
	if err != nil {
		return caller.Err() == nil && !errors.Is(err, context.Canceled)
	}

	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError
}

// cancelOnClose ends a try's time when its answer is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	defer c.cancel()

	return c.ReadCloser.Close()
}
