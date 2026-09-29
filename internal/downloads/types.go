// Package downloads owns durable server download jobs and their transfer state.
package downloads

import (
	"errors"
	"time"
)

var (
	ErrInvalid                = errors.New("invalid download request")
	ErrNotFound               = errors.New("download not found")
	ErrState                  = errors.New("download action is not valid in this state")
	ErrIdempotency            = errors.New("idempotency key was used with another request")
	ErrRestart                = errors.New("the download must restart from zero")
	ErrBudget                 = errors.New("download storage budget exceeded")
	ErrBusy                   = errors.New("download job limit reached")
	ErrDestinationUnavailable = errors.New("download destination is unavailable")
	errAuthority              = errors.New("download authority is no longer valid")
	errSourceChanged          = errors.New("download source configuration changed")
)

type Input struct {
	ProfileID       string `json:"profile_id"`
	EntryID         string `json:"entry_id"`
	DestinationID   string `json:"destination_id"`
	DestinationPath string `json:"destination_path"`
	Connections     *int   `json:"connections,omitempty"`
	BytesPerSecond  *int64 `json:"bytes_per_second,omitempty"`
}

type JobError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

func (e *JobError) Error() string { return e.Message }

type Download struct {
	ID               string    `json:"id"`
	ProfileID        *string   `json:"profile_id"`
	EntryID          string    `json:"entry_id"`
	Name             string    `json:"name"`
	DestinationID    string    `json:"destination_id"`
	DestinationPath  string    `json:"destination_path"`
	SavedName        *string   `json:"saved_name"`
	State            string    `json:"state"`
	TransferredBytes int64     `json:"transferred_bytes"`
	TotalBytes       *int64    `json:"total_bytes"`
	BytesPerSecond   *float64  `json:"bytes_per_second"`
	ETASeconds       *float64  `json:"eta_seconds"`
	Connections      int       `json:"connections"`
	FallbackReason   *string   `json:"fallback_reason"`
	FileAvailable    bool      `json:"file_available"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
	Error            *JobError `json:"error"`
}

type Query struct {
	State, Cursor string
	Limit         int
}
type Page struct {
	Downloads  []Download `json:"downloads"`
	NextCursor *string    `json:"next_cursor"`
}
type Destination struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	AvailableBytes *int64 `json:"available_bytes"`
	Available      bool   `json:"available"`
	Browseable     bool   `json:"browseable"`
}

type MountedRoot struct{ ID, Label, Path string }
type Folder struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type FolderPage struct {
	DestinationID string   `json:"destination_id"`
	Path          string   `json:"path"`
	Folders       []Folder `json:"folders"`
}

type Limits struct {
	StorageBudgetBytes  int64
	FreeReserveBytes    int64
	UnknownSizeMaxBytes int64
	RequestTimeout      time.Duration
	MaxAttempts         int
	RetryBase           time.Duration
	AuthInterval        time.Duration
	CheckpointInterval  time.Duration
}

func defaults(l Limits) Limits {
	if l.StorageBudgetBytes <= 0 {
		l.StorageBudgetBytes = 100 << 30
	}
	if l.FreeReserveBytes == 0 {
		l.FreeReserveBytes = 5 << 30
	}
	if l.FreeReserveBytes < 0 {
		l.FreeReserveBytes = 0
	}
	if l.UnknownSizeMaxBytes <= 0 {
		l.UnknownSizeMaxBytes = 20 << 30
	}
	if l.RequestTimeout <= 0 {
		l.RequestTimeout = 24 * time.Hour
	}
	if l.MaxAttempts <= 0 {
		l.MaxAttempts = 4
	}
	if l.MaxAttempts > 8 {
		l.MaxAttempts = 8
	}
	if l.RetryBase <= 0 {
		l.RetryBase = time.Second
	}
	if l.AuthInterval <= 0 {
		l.AuthInterval = time.Second
	}
	if l.CheckpointInterval <= 0 {
		l.CheckpointInterval = time.Second
	}
	return l
}

type job struct {
	Download
	generation                  int
	creator, lease              string
	owner                       bool
	path                        string
	requested                   int
	bps                         int64
	etag                        string
	ranges, restart, publishing bool
	checksum                    string
	mountedName                 string
}

type segment struct {
	number           int
	start, end, done int64
}
