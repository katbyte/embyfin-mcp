package tmdb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// quick is Retrying without the waits, and with a try's time short enough
// for a test to run it out.
func quick() http.RoundTripper {
	return retrying{next: http.DefaultTransport, pauses: make([]time.Duration, len(retryPauses)), attempt: 300 * time.Millisecond}
}

// A read TMDB failed in a way that passes is tried again: one connection
// reset on one film's titles used to be the end of TMDB for a whole sweep.
// A reset, a try that ran out of time, a 429 and a 5xx pass; a 404 is an
// answer, a POST is sent once, and a failure through every try is handed
// back.
func TestRetryingTriesAgainWhatPasses(t *testing.T) {
	t.Parallel()

	tries := len(retryPauses) + 1
	for _, tc := range []struct {
		name     string
		method   string
		fail     int // the tries that fail before one answers
		failWith func(w http.ResponseWriter)
		status   int // what the caller gets
		asked    int // how many tries the server saw
	}{
		{"a connection reset", http.MethodGet, 1, reset, http.StatusOK, 2},
		{"a try that runs out of time", http.MethodGet, 1, func(http.ResponseWriter) { time.Sleep(time.Second) }, http.StatusOK, 2},
		{"a 502 twice", http.MethodGet, 2, status(http.StatusBadGateway), http.StatusOK, 3},
		{"a 429", http.MethodGet, 1, status(http.StatusTooManyRequests), http.StatusOK, 2},
		{"a 502 through every try", http.MethodGet, tries, status(http.StatusBadGateway), http.StatusBadGateway, tries},
		{"a 404, which is an answer", http.MethodGet, 1, status(http.StatusNotFound), http.StatusNotFound, 1},
		{"a POST", http.MethodPost, 1, status(http.StatusBadGateway), http.StatusBadGateway, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var asked atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if int(asked.Add(1)) <= tc.fail {
					tc.failWith(w)

					return
				}
				if _, err := io.WriteString(w, `{"id":1}`); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(srv.Close)

			req, err := http.NewRequestWithContext(t.Context(), tc.method, srv.URL, strings.NewReader(""))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := quick().RoundTrip(req)
			if err != nil {
				t.Fatalf("round trip: %v (after %d tries)", err, asked.Load())
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if err := resp.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.status || int(asked.Load()) != tc.asked {
				t.Errorf("HTTP %d (%s) after %d tries, want %d after %d", resp.StatusCode, body, asked.Load(), tc.status, tc.asked)
			}
		})
	}
}

// reset drops the connection without an answer, as a peer resetting it does.
func reset(w http.ResponseWriter) {
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		panic(err)
	}
	if err := conn.Close(); err != nil {
		panic(err)
	}
}

// status answers with a status and nothing else.
func status(code int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) { http.Error(w, http.StatusText(code), code) }
}

// scripted answers each read with the next status in its script, 0 being a
// failure with no answer, and counts the reads it saw.
type scripted struct {
	script []int
	seen   int
}

func (s *scripted) RoundTrip(r *http.Request) (*http.Response, error) {
	code := s.script[s.seen]
	s.seen++
	if code == 0 {
		return nil, errors.New("connection reset by peer")
	}

	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
}

// The breaker takes TMDB to be down only after reads failing one after
// another: an answer between them - a 404 is one - starts the count again,
// and a read the caller gave up on is none of TMDB's doing. Once down, a read
// is refused at once, saying why, until the rest has passed and one read is
// let through: an answer to it closes the breaker, a failure opens it again.
func TestBreakerTakesTMDBDownOnlyAfterFailuresInARow(t *testing.T) {
	t.Parallel()

	next := &scripted{script: []int{502, 0, 404, 502, 502, 429, 502, 200, 200}}
	clock := time.Unix(0, 0)
	b := &Breaker{next: next, now: func() time.Time { return clock }}
	read := func(ctx context.Context) (int, error) {
		t.Helper()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://tmdb.invalid/3/movie/1", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := b.RoundTrip(req)
		if err != nil {
			return 0, err
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}

		return resp.StatusCode, nil
	}

	// a 502 and a reset, then an answer: never three in a row
	for i, want := range []int{502, 0, 404, 502, 502} {
		code, err := read(t.Context())
		if code != want || (want == 0) != (err != nil) {
			t.Fatalf("read %d = %d, %v, want %d", i, code, err, want)
		}
	}
	// a read given up on reaches TMDB but is not counted against it
	gaveUp, cancel := context.WithCancel(t.Context())
	cancel()
	failing := &Breaker{next: roundTripFunc(func(r *http.Request) (*http.Response, error) { return nil, r.Context().Err() })}
	for range 5 {
		req, err := http.NewRequestWithContext(gaveUp, http.MethodGet, "http://tmdb.invalid/", http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := failing.RoundTrip(req)
		if resp != nil {
			if cerr := resp.Body.Close(); cerr != nil {
				t.Fatal(cerr)
			}
		}
		if err == nil || errors.As(err, new(*DownError)) {
			t.Fatalf("a read given up on = %v, want its own error and TMDB not taken down", err)
		}
	}
	// the third failure in a row, a 429, takes TMDB down
	if code, err := read(t.Context()); code != 429 || err != nil {
		t.Fatalf("the third failure = %d, %v", code, err)
	}
	seen := next.seen
	var down *DownError
	if _, err := read(t.Context()); !errors.As(err, &down) || down.Failed != 3 || down.Last.Error() != "HTTP 429" || next.seen != seen {
		t.Fatalf("a read with TMDB down = %v (TMDB saw %d reads, had seen %d), want refused at once", err, next.seen, seen)
	}
	if !strings.Contains(down.Error(), "after 3 reads in a row failed, the last with HTTP 429") {
		t.Errorf("the refusal says %q", down.Error())
	}
	// short of the rest, still refused; past it, one read goes through, and
	// its failure opens the breaker again for another rest
	clock = clock.Add(breakerRest - time.Second)
	if _, err := read(t.Context()); !errors.As(err, &down) {
		t.Fatalf("a read short of the rest = %v, want refused", err)
	}
	clock = clock.Add(time.Second)
	if code, err := read(t.Context()); code != 502 || err != nil {
		t.Fatalf("the read let through = %d, %v", code, err)
	}
	if _, err := read(t.Context()); !errors.As(err, &down) || down.Failed != 4 {
		t.Fatalf("after the read let through failed = %v, want refused again", err)
	}
	// and an answer to the next one let through closes it
	clock = clock.Add(breakerRest)
	for i := range 2 {
		if code, err := read(t.Context()); code != 200 || err != nil {
			t.Fatalf("read %d after TMDB came back = %d, %v", i, code, err)
		}
	}
	if next.seen != len(next.script) {
		t.Errorf("TMDB saw %d reads, want %d", next.seen, len(next.script))
	}
}

// A read that fails while TMDB answers another says nothing of TMDB being
// down: sent a few at a time, reads failing through their retries finished
// after the answers beside them, and three such took an answering TMDB to be
// down. Failures with no answer beside them still count.
func TestBreakerLeavesAFailureBesideAnAnswer(t *testing.T) {
	t.Parallel()

	release, arrived := make(chan struct{}), make(chan struct{}, breakerAfter)
	b := &Breaker{next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/slow":
			arrived <- struct{}{}
			<-release

			return nil, errors.New("connection reset by peer")
		case "/fail":
			return nil, errors.New("connection reset by peer")
		}

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}, Request: r}, nil
	})}
	read := func(path string) error {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://tmdb.invalid"+path, http.NoBody)
		if err != nil {
			return err
		}
		resp, err := b.RoundTrip(req)
		if err != nil {
			return err
		}

		return resp.Body.Close()
	}
	// breakerAfter reads in flight, an answer beside them, and then they fail
	errs := make(chan error, breakerAfter)
	var started sync.WaitGroup
	for range breakerAfter {
		started.Go(func() { errs <- read("/slow") })
	}
	// every slow read is past the breaker and at TMDB before the answer
	for range breakerAfter {
		<-arrived
	}
	if err := read("/ok"); err != nil {
		t.Fatalf("the answer = %v", err)
	}
	close(release)
	started.Wait()
	close(errs)
	for err := range errs {
		if err == nil || errors.As(err, new(*DownError)) {
			t.Fatalf("a read failing beside an answer = %v, want its own failure", err)
		}
	}
	// none of them counted: breakerAfter failures more, one after another,
	// take TMDB down, and not one fewer
	for i := range breakerAfter {
		if err := read("/fail"); err == nil || errors.As(err, new(*DownError)) {
			t.Fatalf("failure %d = %v, want it sent", i+1, err)
		}
	}
	if err := read("/ok"); !errors.As(err, new(*DownError)) {
		t.Fatalf("after %d failures in a row = %v, want TMDB taken down", breakerAfter, err)
	}
}

// A read that ran out of time - the client's own timeout, a slow 5xx waited
// out past it - is TMDB failing, not the caller giving up: counted as
// neither, a TMDB that hung was never taken to be down.
func TestBreakerCountsAReadThatRanOutOfTime(t *testing.T) {
	t.Parallel()

	slow := &Breaker{next: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()

		return nil, r.Context().Err()
	})}
	for range breakerAfter {
		ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://tmdb.invalid/", http.NoBody)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		resp, err := slow.RoundTrip(req)
		cancel()
		if resp != nil {
			if cerr := resp.Body.Close(); cerr != nil {
				t.Fatal(cerr)
			}
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("a read that ran out of time = %v", err)
		}
	}
	// given time enough to be refused, not so much a hang holds the test
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://tmdb.invalid/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := slow.RoundTrip(req)
	if resp != nil {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Fatal(cerr)
		}
	}
	if !errors.As(err, new(*DownError)) {
		t.Errorf("after %d reads ran out of time = %v, want TMDB taken to be down", breakerAfter, err)
	}
}

// roundTripFunc is a transport made of a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
