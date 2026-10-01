package mediabroker

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type heldResultBody struct {
	entered chan struct{}
	release chan struct{}
	reader  io.Reader
	started bool
}

func (b *heldResultBody) Read(p []byte) (int, error) {
	if !b.started {
		b.started = true
		close(b.entered)
		<-b.release
	}
	return b.reader.Read(p)
}

type countedResultBody struct {
	reader io.Reader
	reads  atomic.Int64
}

func (b *countedResultBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	return b.reader.Read(p)
}

func TestResultUploadAdmissionAndRetry(t *testing.T) {
	for _, replaceLease := range []bool{false, true} {
		name := "same lease"
		if replaceLease {
			name = "replacement lease"
		}
		t.Run(name, func(t *testing.T) {
			b, j := testBroker(t, 100)
			claimJob(t, b, j)
			body := &heldResultBody{entered: make(chan struct{}), release: make(chan struct{}), reader: strings.NewReader(`{"state":"invalid"}`)}
			first := make(chan int, 1)
			go func() {
				first <- brokerRequest(t, b, "POST", "/jobs/"+j.id+"/result", j.token, "", "application/json", body).Code
			}()
			select {
			case <-body.entered:
			case <-time.After(time.Second):
				close(body.release)
				t.Fatal("first result upload did not read its body")
			}
			current := j
			if replaceLease {
				_, current = testBroker(t, 100)
				current.id = "job_two"
				current.token = "replacement-capability"
				current.claimed = true
				b.mu.Lock()
				j.cancel()
				b.active = current
				b.mu.Unlock()
			}
			secondBody := &countedResultBody{reader: strings.NewReader(`{"state":"unavailable"}`)}
			second := brokerRequest(t, b, "POST", "/jobs/"+current.id+"/result", current.token, "", "application/json", secondBody)
			close(body.release)
			select {
			case code := <-first:
				if code != http.StatusBadRequest {
					t.Fatalf("malformed upload status=%d", code)
				}
			case <-time.After(time.Second):
				t.Fatal("first result upload did not finish")
			}
			if second.Code != http.StatusTooManyRequests || secondBody.reads.Load() != 0 {
				t.Fatalf("concurrent result buffered: status=%d body_reads=%d", second.Code, secondBody.reads.Load())
			}
			retry := brokerRequest(t, b, "POST", "/jobs/"+current.id+"/result", current.token, "", "application/json", strings.NewReader(`{"state":"unavailable"}`))
			if retry.Code != http.StatusNoContent {
				t.Fatalf("retry after invalid upload status=%d", retry.Code)
			}
			if replay := brokerRequest(t, b, "POST", "/jobs/"+current.id+"/result", current.token, "", "application/json", strings.NewReader(`{"state":"unavailable"}`)); replay.Code != http.StatusUnauthorized {
				t.Fatalf("completed capability replay status=%d", replay.Code)
			}
		})
	}
}
