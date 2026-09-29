package downloadfs

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

const testJobID = "synthetic_job_123456"

func storageFixture(t *testing.T) (*Store, *Job, string) {
	t.Helper()
	path := t.TempDir()
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	job, err := store.OpenJob(testJobID, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { job.Close() })
	return store, job, path
}

func writePart(t *testing.T, job *Job, body []byte) {
	t.Helper()
	file, err := job.OpenPart()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(body); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSafeNamePortableAndUnicode(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{{"../../secret.txt", "_.._secret.txt"}, {`..\secret.txt`, "_secret.txt"}, {"CON", "_CON"}, {"con.txt", "_con.txt"}, {"NUL.tar.gz", "_NUL.tar.gz"}, {"LPT1.txt", "_LPT1.txt"}, {"COM².zip", "_COM².zip"}, {"name. ", "name"}, {"\x00\r\n", "download"}, {".", "download"}, {".part", "part"}, {"a:b?c*d|e<f>g\".txt", "a_b_c_d_e_f_g_.txt"}, {"cafe\u0301 雪.txt", "café 雪.txt"}, {"safe\u202Egnp.exe", "safegnp.exe"}} {
		t.Run(tc.raw, func(t *testing.T) {
			got := SafeName(tc.raw)
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			if SafeName(got) != got || !utf8.ValidString(got) {
				t.Fatal("name is not stable valid UTF-8")
			}
		})
	}
	for _, raw := range []string{strings.Repeat("雪", 100) + ".txt", strings.Repeat("a", 500), "bad\xffname"} {
		name := SafeName(raw)
		if len(name) > 180 || !utf8.ValidString(name) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
			t.Fatal("name bound failed", name)
		}
	}
}

func TestPartResumeResetAndAtomicPublish(t *testing.T) {
	store, job, path := storageFixture(t)
	payload := []byte("synthetic download bytes")
	writePart(t, job, payload)
	file, err := job.OpenPart()
	if err != nil {
		t.Fatal(err)
	}
	original, err := io.ReadAll(file)
	file.Close()
	if err != nil || !bytes.Equal(original, payload) {
		t.Fatal("reopening truncated partial", err)
	}
	if err := job.Publish("file.bin", int64(len(payload)+1)); !errors.Is(err, ErrSize) {
		t.Fatal("incorrect size published", err)
	}
	if _, err := job.OpenCompleted("file.bin"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unverified file visible", err)
	}
	if err := job.ResetPart(); err != nil {
		t.Fatal(err)
	}
	file, err = job.OpenPart()
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	file.Close()
	if err != nil || info.Size() != 0 {
		t.Fatal("explicit reset did not truncate", err)
	}
	writePart(t, job, payload)
	if err := job.Publish("file.bin", int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(path, testJobID, ".part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("publication left partial", err)
	}
	completed, err := job.OpenCompleted("file.bin")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(completed)
	completed.Close()
	if err != nil || sha256.Sum256(body) != sha256.Sum256(payload) {
		t.Fatal("published content changed", err)
	}
	if used, err := store.UsedBytes(); err != nil || used != int64(len(payload)) {
		t.Fatal("incorrect storage usage", used, err)
	}
	if free, err := store.AvailableBytes(); err != nil || free == nil || *free <= 0 {
		t.Fatal("free space unavailable", free, err)
	}
	job.Close()
	store.Close()
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, err := reopened.OpenJob(testJobID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	completed, err = restored.OpenCompleted("file.bin")
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(completed)
	completed.Close()
	if err != nil || !bytes.Equal(body, payload) {
		t.Fatal("completed file did not survive reopen", err)
	}
	if err := restored.RemoveCompleted("file.bin"); err != nil {
		t.Fatal(err)
	}
	if err := restored.RemoveCompleted("file.bin"); err != nil {
		t.Fatal("removal is not idempotent", err)
	}
}

func TestExistingCompletedFileIsNeverOverwritten(t *testing.T) {
	_, job, _ := storageFixture(t)
	writePart(t, job, []byte("original"))
	if err := job.Publish("same.bin", 8); err != nil {
		t.Fatal(err)
	}
	writePart(t, job, []byte("replacement"))
	if err := job.Publish("same.bin", 11); !errors.Is(err, ErrExists) {
		t.Fatal("existing completed target not protected", err)
	}
	file, err := job.OpenCompleted("same.bin")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(file)
	file.Close()
	if string(body) != "original" {
		t.Fatal("existing target was overwritten")
	}
	part, err := job.OpenPart()
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(part)
	part.Close()
	if string(body) != "replacement" {
		t.Fatal("failed publication lost partial")
	}
}

func TestInterruptedPublishRecoveryOnlyRemovesMatchingPartial(t *testing.T) {
	store, job, root := storageFixture(t)
	payload := []byte("publication survived")
	writePart(t, job, payload)
	if err := job.root.Link(".part", "finished.bin"); err != nil {
		t.Fatal(err)
	}
	job.Close()
	job, err := store.OpenJob(testJobID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer job.Close()
	if _, err := job.OpenPart(); !errors.Is(err, ErrUnsafe) {
		t.Fatal("interrupted publish remained writable", err)
	}
	if err := job.Publish("finished.bin", int64(len(payload)+1)); !errors.Is(err, ErrSize) {
		t.Fatal("recovery ignored expected size", err)
	}
	if _, err := os.Lstat(filepath.Join(root, testJobID, ".part")); err != nil {
		t.Fatal("failed recovery removed partial", err)
	}
	if err := job.Publish("finished.bin", int64(len(payload))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, testJobID, ".part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovery kept partial", err)
	}
	file, err := job.OpenCompleted("finished.bin")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(file)
	file.Close()
	if err != nil || sha256.Sum256(body) != sha256.Sum256(payload) {
		t.Fatal("recovery changed bytes", err)
	}
}

func TestPublishRecoveryRefusesUnrelatedThirdHardlink(t *testing.T) {
	_, job, root := storageFixture(t)
	writePart(t, job, []byte("preserved"))
	if err := job.root.Link(".part", "finished.bin"); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, testJobID, ".part"), filepath.Join(root, "unrelated-link")); err != nil {
		t.Fatal(err)
	}
	if err := job.Publish("finished.bin", 9); !errors.Is(err, ErrUnsafe) {
		t.Fatal("three-link publication recovered", err)
	}
	for _, path := range []string{filepath.Join(root, testJobID, ".part"), filepath.Join(root, testJobID, "finished.bin"), filepath.Join(root, "unrelated-link")} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "preserved" {
			t.Fatal("failed recovery removed or changed existing data", err)
		}
	}
}

func TestInvalidPathsAndNamesFailBeforeFilesystemAccess(t *testing.T) {
	store, job, _ := storageFixture(t)
	for _, id := range []string{"", "short", "..", "../outside_123456789", `..\outside_123456789`, "/absolute_123456789", strings.Repeat("x", 129), "CON"} {
		if _, err := store.OpenJob(id, true); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid job path accepted", id, err)
		}
	}
	for _, name := range []string{"", ".part", "..", "../outside", `..\outside`, "CON.txt", "trailing.", strings.Repeat("a", 181), "a/b", "x:y"} {
		if err := job.Publish(name, 0); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid publish path accepted", name, err)
		}
		if _, err := job.OpenCompleted(name); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid read path accepted", name, err)
		}
		if err := job.RemoveCompleted(name); !errors.Is(err, ErrInvalid) {
			t.Fatal("invalid remove path accepted", name, err)
		}
	}
	if err := job.Publish("valid.bin", -1); !errors.Is(err, ErrInvalid) {
		t.Fatal("negative size accepted", err)
	}
	if err := job.ResetPart(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reset created a missing partial", err)
	}
	if err := job.RemovePart(); err != nil {
		t.Fatal(err)
	}
}

func TestPerJobFilenameCollisionsAreIndependent(t *testing.T) {
	store, first, _ := storageFixture(t)
	second, err := store.OpenJob("another_job_123456", true)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, job := range []*Job{first, second} {
		writePart(t, job, []byte("same name"))
		if err := job.Publish(SafeName("CON.txt"), 9); err != nil {
			t.Fatal(err)
		}
	}
	if used, err := store.UsedBytes(); err != nil || used != 18 {
		t.Fatal(used, err)
	}
}

func symlinkOrSkip(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func TestSymlinkPartCannotReadWriteOrRemoveOutsideRoot(t *testing.T) {
	_, job, root := storageFixture(t)
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, []byte("preserve outside"), 0600); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, outside, filepath.Join(root, testJobID, ".part"))
	if _, err := job.OpenPart(); err == nil {
		t.Fatal("opened outside partial")
	}
	if err := job.ResetPart(); err == nil {
		t.Fatal("reset outside partial")
	}
	if err := job.Publish("file.bin", 16); err == nil {
		t.Fatal("published outside partial")
	}
	if err := job.RemovePart(); err == nil {
		t.Fatal("accepted partial symlink")
	}
	body, err := os.ReadFile(outside)
	if err != nil || string(body) != "preserve outside" {
		t.Fatal("outside file changed", err)
	}
}

func TestSymlinkDirectoriesAndCompletedTargetsRejected(t *testing.T) {
	store, job, root := storageFixture(t)
	outside := t.TempDir()
	symlinkOrSkip(t, outside, filepath.Join(root, "symlink_job_123456"))
	if _, err := store.OpenJob("symlink_job_123456", false); err == nil {
		t.Fatal("opened symlink job root")
	}
	if _, err := store.UsedBytes(); err == nil {
		t.Fatal("usage traversal followed symlink")
	}
	symlinkOrSkip(t, filepath.Join(outside, "untouched"), filepath.Join(root, testJobID, "linked.bin"))
	if _, err := job.OpenCompleted("linked.bin"); err == nil {
		t.Fatal("opened completed symlink")
	}
	if err := job.RemoveCompleted("linked.bin"); err == nil {
		t.Fatal("removed a nonregular completed file")
	}
	writePart(t, job, []byte("safe"))
	if err := job.Publish("linked.bin", 4); err == nil {
		t.Fatal("replaced existing completed symlink")
	}
	if _, err := os.Lstat(filepath.Join(outside, "untouched")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("outside symlink target created", err)
	}
}

func TestHardlinkedPartialIsNotWritable(t *testing.T) {
	_, job, root := storageFixture(t)
	outside := filepath.Join(t.TempDir(), "original.bin")
	if err := os.WriteFile(outside, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(root, testJobID, ".part")); err != nil {
		t.Fatal(err)
	}
	if _, err := job.OpenPart(); !errors.Is(err, ErrUnsafe) {
		t.Fatal("hardlinked partial opened", err)
	}
	if err := job.ResetPart(); !errors.Is(err, ErrUnsafe) {
		t.Fatal("hardlinked partial reset", err)
	}
	if err := job.Publish("file.bin", 9); !errors.Is(err, ErrUnsafe) {
		t.Fatal("hardlinked partial published", err)
	}
	if err := job.RemovePart(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(outside)
	if err != nil || string(body) != "preserved" {
		t.Fatal("hardlink changed outside file", err)
	}
}

func TestPinnedJobRootDoesNotFollowDirectoryReplacement(t *testing.T) {
	store, job, root := storageFixture(t)
	oldPath := filepath.Join(root, testJobID)
	moved := filepath.Join(root, "moved_job_123456")
	if err := os.Rename(oldPath, moved); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows blocked renaming an open directory: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.Mkdir(oldPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldPath, ".part"), []byte("replacement preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	writePart(t, job, []byte("pinned"))
	if err := job.Publish("file.bin", 6); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(moved, "file.bin"))
	if err != nil || string(body) != "pinned" {
		t.Fatal("job did not retain pinned directory", err)
	}
	body, err = os.ReadFile(filepath.Join(oldPath, ".part"))
	if err != nil || string(body) != "replacement preserved" {
		t.Fatal("replacement directory was modified", err)
	}
	if used, err := store.UsedBytes(); err != nil || used != 27 {
		t.Fatal("pinned storage usage wrong", used, err)
	}
}

func TestUsageBoundsAndClosedSpaceQuery(t *testing.T) {
	store, job, root := storageFixture(t)
	writePart(t, job, []byte("x"))
	entries := 100000
	if _, err := usedBytes(store.root, 0, &entries); !errors.Is(err, ErrLimit) {
		t.Fatal("entry traversal cap failed", err)
	}
	deep := root
	for i := 0; i < 9; i++ {
		deep = filepath.Join(deep, "level")
		if err := os.Mkdir(deep, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.UsedBytes(); !errors.Is(err, ErrLimit) {
		t.Fatal("depth traversal cap failed", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if free, err := store.AvailableBytes(); err == nil || free != nil {
		t.Fatal("closed storage claimed available space", free, err)
	}
}
