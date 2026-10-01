package thumbnails

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"

	"github.com/themartz90/folderhug/internal/upstream"
)

var (
	ErrInvalid  = errors.New("invalid thumbnail request")
	ErrNotFound = errors.New("thumbnail not found")
	ErrBusy     = errors.New("thumbnail queue is full")
	ErrClosed   = errors.New("thumbnail service is closed")
	ErrImage    = errors.New("image cannot be thumbnailed within configured limits")
)

type Fetcher interface {
	Do(context.Context, *upstream.Root, string, string, upstream.Options) (*http.Response, error)
}

type Config struct {
	CacheDir string
	MaxBytes int64
}

type Thumbnail struct {
	ID      string `json:"id"`
	EntryID string `json:"entry_id"`
	State   string `json:"state"`
}

// File keeps its cache entry pinned until the response body has been closed.
type File struct {
	*os.File
	once     sync.Once
	release  func()
	closeErr error
}

func (f *File) Close() error {
	f.once.Do(func() { f.closeErr = f.File.Close(); f.release() })
	return f.closeErr
}
