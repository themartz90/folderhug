//go:build !linux

package downloadfs

import "os"

func DiscoverMounts([]string) ([]DiscoveredMount, error)        { return nil, nil }
func verifyOpenedMount(string, *os.Root, []string) (int, error) { return 0, ErrUnsafe }
func verifyMountChild(*os.Root, int) error                      { return ErrUnsafe }
func verifyMountFile(*os.File, int) error                       { return ErrUnsafe }
