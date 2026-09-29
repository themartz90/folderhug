package thumbnails

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"

	"github.com/themartz90/folderhug/internal/auth"
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

type VideoRequest struct {
	ProfileID  string
	EntryID    string
	Generation int
	Principal  auth.Principal
}

// VideoGenerator returns a JPEG from the isolated broker, never source credentials.
// Implementations must stop on context cancellation.
type VideoGenerator interface {
	Generate(context.Context, VideoRequest) ([]byte, error)
}

type Config struct {
	CacheDir string
	MaxBytes int64
	Video    VideoGenerator
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
