//go:build unix

package downloadfs

import (
	"math"
	"os"

	"golang.org/x/sys/unix"
)

const safeOpenFlags = unix.O_NOFOLLOW | unix.O_NONBLOCK

func linkCount(file *os.File) (uint64, error) {
	var info unix.Stat_t
	err := unix.Fstat(int(file.Fd()), &info)
	return uint64(info.Nlink), err
}

func availableBytes(directory *os.File) (int64, error) {
	var info unix.Statfs_t
	if err := unix.Fstatfs(int(directory.Fd()), &info); err != nil {
		return 0, err
	}
	if info.Bsize < 0 {
		return 0, ErrUnsafe
	}
	blocks, size := uint64(info.Bavail), uint64(info.Bsize)
	if size != 0 && blocks > math.MaxInt64/size {
		return math.MaxInt64, nil
	}
	return int64(blocks * size), nil
}

func syncDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
