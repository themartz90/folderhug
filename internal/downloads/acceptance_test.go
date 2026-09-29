package downloads

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themartz90/folderhug/internal/auth"
	"github.com/themartz90/folderhug/internal/catalog"
	"github.com/themartz90/folderhug/internal/downloadfs"
	"github.com/themartz90/folderhug/internal/events"
	"github.com/themartz90/folderhug/internal/listing"
	"github.com/themartz90/folderhug/internal/profiles"
	"github.com/themartz90/folderhug/internal/store"
	"github.com/themartz90/folderhug/internal/testfixture"
	"github.com/themartz90/folderhug/internal/upstream"
)

func acceptanceEnabled(t *testing.T) {
	t.Helper()
	if os.Getenv("HTTPWRAPPER_DOWNLOAD_ACCEPTANCE") != "1" {
		t.Skip("set HTTPWRAPPER_DOWNLOAD_ACCEPTANCE=1 for bounded synthetic acceptance checks")
	}
}

type acceptanceFetcher struct {
	base   *fixtureFetcher
	delay  time.Duration
	gate   chan struct{}
	mu     sync.Mutex
	ranges []string
}

func (f *acceptanceFetcher) Do(ctx context.Context, root *upstream.Root, method, path string, options upstream.Options) (*http.Response, error) {
	f.mu.Lock()
	f.ranges = append(f.ranges, options.Headers.Get("Range"))
	f.mu.Unlock()
	response, err := f.base.Do(ctx, root, method, path, options)
	if err == nil && method == "GET" && response.ContentLength != 1 && f.delay > 0 {
		response.Body = &acceptanceBody{ReadCloser: response.Body, ctx: ctx, delay: f.delay, gate: f.gate}
	}
	return response, err
}

type acceptanceBody struct {
	io.ReadCloser
	ctx   context.Context
	delay time.Duration
	gate  chan struct{}
}

func (b *acceptanceBody) Read(p []byte) (int, error) {
	if b.gate != nil {
		select {
		case b.gate <- struct{}{}:
			defer func() { <-b.gate }()
		case <-b.ctx.Done():
			return 0, b.ctx.Err()
		}
	}
	timer := time.NewTimer(b.delay)
	defer timer.Stop()
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-timer.C:
	}
	return b.ReadCloser.Read(p[:min(len(p), 16<<10)])
}

func TestAcceptanceConnectionTiming(t *testing.T) {
	acceptanceEnabled(t)
	for _, shared := range []bool{false, true} {
		name := "per-connection"
		if shared {
			name = "shared-source"
		}
		t.Run(name, func(t *testing.T) {
			times := map[int]time.Duration{}
			for _, connections := range []int{1, 2, 4} {
				h := fixture(t, 1<<20, Limits{CheckpointInterval: 50 * time.Millisecond, AuthInterval: time.Second})
				f := &acceptanceFetcher{base: h.fetcher, delay: 15 * time.Millisecond}
				if shared {
					f.gate = make(chan struct{}, 1)
				}
				h.s.fetcher = f
				j := h.create(t, connections, "timing", h.owner)
				start := time.Now()
				h.start(t)
				done := h.wait(t, j.ID, func(d Download) bool {
					return d.State == "completed" || d.State == "failed" || d.State == "interrupted"
				})
				times[connections] = time.Since(start)
				if done.State != "completed" || done.Connections != connections || h.hash(t, j.ID) != sha256.Sum256(h.data) {
					t.Fatalf("%d connection transfer failed: %+v", connections, done)
				}
				h.s.Close()
				t.Logf("%d connections: %s, SHA-256 %x", connections, times[connections], sha256.Sum256(h.data))
			}
			if shared {
				for _, n := range []int{2, 4} {
					if times[n] < times[1]*3/4 || times[n] > times[1]*7/4 {
						t.Fatalf("shared source timing outside tolerance: %v", times)
					}
				}
			} else if times[2] >= times[1]*85/100 || times[4] >= times[1]*65/100 {
				t.Fatalf("per-connection limit did not improve with parallelism: %v", times)
			}
		})
	}
}

type acceptanceState struct {
	Profile profiles.Profile
	Entry   listing.Entry
	Owner   auth.Principal
	JobID   string
}

func acceptancePersistentHarness(t *testing.T, directory string, create bool) (*harness, acceptanceState) {
	t.Helper()
	db, err := store.Open(filepath.Join(directory, "data"))
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(directory, "key")
	var vault *auth.Vault
	if create {
		vault, err = auth.CreateVault(keyPath)
	} else {
		vault, err = auth.LoadVault(keyPath)
	}
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	a := auth.New(db.DB)
	p := profiles.New(db.DB, vault)
	var state acceptanceState
	if create {
		state.Owner = auth.Principal{Owner: true, CreatorID: "owner", SessionID: "acceptance-owner", ExpiresAt: time.Now().Add(time.Hour)}
		if _, err = db.DB.Exec(`INSERT INTO auth_sessions(id,secret_hash,csrf_token,expires_at) VALUES(?,?,?,?)`, state.Owner.SessionID, []byte("synthetic-session"), "synthetic-csrf", state.Owner.ExpiresAt.Unix()); err != nil {
			t.Fatal(err)
		}
		state.Profile, err = p.Create(context.Background(), profiles.Input{Name: "Synthetic restart", RootURL: "https://source.example/", Connections: 4}, state.Owner)
		if err != nil {
			t.Fatal(err)
		}
		state.Entry = listing.Entry{ID: listing.ID(state.Profile.ID, state.Profile.Generation, "file.bin"), ProfileID: state.Profile.ID, ParentID: "root", Name: "file.bin", EscapedPath: "file.bin", Kind: "file", Category: "other", Provenance: "response_headers"}
		body, _ := json.Marshal(state.Entry)
		if _, err = db.DB.Exec(`INSERT INTO catalog_entries(profile_id,generation,id,parent_id,escaped_path,metadata) VALUES(?,?,?,?,?,?)`, state.Profile.ID, state.Profile.Generation, state.Entry.ID, "root", "file.bin", string(body)); err != nil {
			t.Fatal(err)
		}
	} else {
		body, err := os.ReadFile(filepath.Join(directory, "state.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(body, &state); err != nil {
			t.Fatal(err)
		}
	}
	data := make([]byte, 4<<20)
	for i := range data {
		data[i] = byte(i % 251)
	}
	f := &fixtureFetcher{handler: testfixture.Handler(testfixture.Config{Files: map[string]testfixture.File{"/file.bin": {Data: data}}})}
	c := catalog.New(db.DB, p, f)
	filesPath := filepath.Join(directory, "files")
	if err = os.MkdirAll(filesPath, 0700); err != nil {
		t.Fatal(err)
	}
	files, err := downloadfs.Open(filesPath)
	if err != nil {
		t.Fatal(err)
	}
	limits := Limits{FreeReserveBytes: -1, CheckpointInterval: 20 * time.Millisecond, AuthInterval: time.Second}
	h := &harness{db: db, profiles: p, auth: a, catalog: c, files: files, fetcher: f, owner: state.Owner, profile: state.Profile, entry: state.Entry, data: data, limits: limits}
	h.s = New(db.DB, p, a, c, f, events.New(a), files, limits)
	t.Cleanup(func() { h.s.Close(); p.Close(); files.Close(); db.Close() })
	return h, state
}

func TestAcceptanceDownloadChild(t *testing.T) {
	acceptanceEnabled(t)
	directory := os.Getenv("HTTPWRAPPER_ACCEPTANCE_CHILD_ROOT")
	if directory == "" {
		t.Skip("subprocess helper")
	}
	h, _ := acceptancePersistentHarness(t, directory, false)
	h.fetcher.delay = 10 * time.Millisecond
	h.start(t)
	// The parent kills this process while its durable download is still active.
	time.Sleep(20 * time.Second)
	t.Fatal("parent did not terminate download subprocess")
}

func TestAcceptanceProcessKillAndResume(t *testing.T) {
	acceptanceEnabled(t)
	directory := t.TempDir()
	h, state := acceptancePersistentHarness(t, directory, true)
	j := h.create(t, 1, "process-kill", h.owner)
	state.JobID = j.ID
	body, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(directory, "state.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestAcceptanceDownloadChild$", "-test.timeout=20s")
	cmd.Env = append(os.Environ(), "HTTPWRAPPER_ACCEPTANCE_CHILD_ROOT="+directory)
	logFile, err := os.Create(filepath.Join(directory, "child.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	partial := h.wait(t, j.ID, func(d Download) bool { return d.TransferredBytes >= 128<<10 })
	if partial.State != "running" || partial.TransferredBytes >= int64(len(h.data)) {
		t.Fatalf("not killed mid-transfer: %+v", partial)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err == nil {
		t.Fatal("child exited without forced termination")
	}
	// Close every parent handle before reopening the durable state.
	h.s.Close()
	h.profiles.Close()
	h.files.Close()
	h.db.Close()
	reopened, _ := acceptancePersistentHarness(t, directory, false)
	f := &acceptanceFetcher{base: reopened.fetcher}
	reopened.s.fetcher = f
	reopened.start(t)
	recovered, err := reopened.s.Get(context.Background(), reopened.owner, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != "interrupted" || recovered.TransferredBytes < partial.TransferredBytes {
		t.Fatalf("lost durable progress: %+v", recovered)
	}
	if _, err = reopened.s.Action(context.Background(), reopened.owner, j.ID, "resume", false); err != nil {
		t.Fatal(err)
	}
	done := reopened.wait(t, j.ID, func(d Download) bool { return d.State == "completed" || d.State == "failed" })
	if done.State != "completed" || reopened.hash(t, j.ID) != sha256.Sum256(reopened.data) {
		t.Fatalf("resume checksum failed: %+v", done)
	}
	f.mu.Lock()
	ranges := append([]string(nil), f.ranges...)
	f.mu.Unlock()
	want := fmt.Sprintf("bytes=%d-%d", recovered.TransferredBytes, len(reopened.data)-1)
	found := false
	for _, r := range ranges {
		if r == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("did not resume durable offset %s: %v", want, ranges)
	}
	t.Logf("Killed after %d durable bytes; reopened at %d; resumed SHA-256 %x", partial.TransferredBytes, recovered.TransferredBytes, sha256.Sum256(reopened.data))
}

func TestAcceptanceLargeLogicalOffset(t *testing.T) {
	acceptanceEnabled(t)
	const offset int64 = (5 << 30) + 123
	const length int64 = 65573
	total := offset + length
	h := fixture(t, 1, Limits{StorageBudgetBytes: 10 << 30})
	h.fetcher.handler = testfixture.Handler(testfixture.Config{Files: map[string]testfixture.File{"/file.bin": testfixture.DeterministicFile(total)}})
	f := &acceptanceFetcher{base: h.fetcher}
	h.s.fetcher = f
	created := h.create(t, 2, "large-offset", h.owner)
	j, err := h.s.load(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	j.TotalBytes = &total
	j.Connections = 2
	j.ranges = true
	j.etag = fmt.Sprintf(`"synthetic-%d-0"`, total)
	if err = h.s.initialize(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if _, err = h.db.DB.Exec(`UPDATE download_segments SET start_byte=?,end_byte=?,durable_bytes=0 WHERE job_id=? AND number=1`, offset, total-1, j.ID); err != nil {
		t.Fatal(err)
	}
	dir, err := h.files.OpenJob(j.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	part, err := dir.OpenPart()
	if err != nil {
		t.Fatal(err)
	}
	defer part.Close()
	if runtime.GOOS == "windows" {
		// NTFS requires the sparse flag before a high-offset write avoids allocation.
		if output, err := exec.Command("fsutil", "sparse", "setflag", part.Name()).CombinedOutput(); err != nil {
			t.Fatalf("enable sparse file before extension: %v %s", err, output)
		}
	}
	if err = part.Truncate(offset); err != nil {
		t.Fatal(err)
	}
	if err = h.s.reserve(j.ID, total, offset, offset); err != nil {
		t.Fatal(err)
	}
	defer h.s.release(j.ID)
	root, err := upstream.NewRoot(h.profile.RootURL, false)
	if err != nil {
		t.Fatal(err)
	}
	segment := segment{number: 1, start: offset, end: total - 1}
	if err = h.s.copySegment(context.Background(), root, nil, j, segment, part, &throttle{}); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, length)
	if _, err = part.ReadAt(got, offset); err != nil {
		t.Fatal(err)
	}
	expected := make([]byte, length)
	for i := range expected {
		expected[i] = byte((uint64(offset) + uint64(i)) % 251)
	}
	if sha256.Sum256(got) != sha256.Sum256(expected) {
		t.Fatal("high-offset range or WriteAt truncated offset")
	}
	info, err := part.Stat()
	if err != nil || info.Size() != total {
		t.Fatal("wrong logical file length")
	}
	f.mu.Lock()
	ranges := append([]string(nil), f.ranges...)
	f.mu.Unlock()
	if len(ranges) != 1 || ranges[0] != "bytes="+strconv.FormatInt(offset, 10)+"-"+strconv.FormatInt(total-1, 10) {
		t.Fatalf("incorrect high-offset request %v", ranges)
	}
	var durable int64
	if err = h.db.DB.QueryRow(`SELECT durable_bytes FROM download_segments WHERE job_id=? AND number=1`, j.ID).Scan(&durable); err != nil || durable != length {
		t.Fatal("wrong durable high-offset count")
	}
	if runtime.GOOS == "windows" {
		output, err := exec.Command("fsutil", "sparse", "queryflag", part.Name()).CombinedOutput()
		if err != nil {
			t.Fatalf("verify sparse flag: %v %s", err, output)
		}
		t.Log(strings.TrimSpace(string(output)))
		output, err = exec.Command("fsutil", "sparse", "queryrange", part.Name()).CombinedOutput()
		if err != nil {
			t.Fatalf("query allocated ranges: %v %s", err, output)
		}
		numbers := regexp.MustCompile(`0x[0-9a-fA-F]+`).FindAllString(string(output), -1)
		if len(numbers) == 0 || len(numbers)%2 != 0 {
			t.Fatalf("unrecognized allocated ranges: %s", output)
		}
		var allocated uint64
		for i := 1; i < len(numbers); i += 2 {
			n, err := strconv.ParseUint(numbers[i][2:], 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			allocated += n
		}
		if allocated > 1<<20 {
			t.Fatalf("sparse fixture allocated %d bytes", allocated)
		}
		t.Logf("Allocated sparse ranges: %d bytes", allocated)
	} else if native := reflect.Indirect(reflect.ValueOf(info.Sys())); native.IsValid() && native.Kind() == reflect.Struct {
		if blocks := native.FieldByName("Blocks"); blocks.IsValid() && blocks.CanInt() {
			allocated := blocks.Int() * 512
			if allocated > 1<<20 {
				t.Fatalf("sparse fixture allocated %d bytes", allocated)
			}
			t.Logf("Allocated filesystem blocks: %d bytes", allocated)
		}
	}
	t.Logf("Logical file %d bytes; payload written %d bytes at %d; SHA-256 %x", total, length, offset, sha256.Sum256(got))
}
