//go:build windows

package downloadfs

import (
	"math"
	"os"

	"golang.org/x/sys/windows"
)

const safeOpenFlags = 0

func linkCount(file *os.File) (uint64, error) {
	var info windows.ByHandleFileInformation
	err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info)
	return uint64(info.NumberOfLinks), err
}

func availableBytes(directory *os.File) (int64, error) {
	buffer := make([]uint16, 32768)
	n, err := windows.GetFinalPathNameByHandle(windows.Handle(directory.Fd()), &buffer[0], uint32(len(buffer)), 0)
	if err != nil {
		return 0, err
	}
	if n >= uint32(len(buffer)) {
		return 0, ErrLimit
	}
	path, err := windows.UTF16PtrFromString(windows.UTF16ToString(buffer[:n]))
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(path, &available, nil, nil); err != nil {
		return 0, err
	}
	if available > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(available), nil
}

// Windows does not provide directory fsync through Go's directory handles.
func syncDirectory(*os.Root) error { return nil }
