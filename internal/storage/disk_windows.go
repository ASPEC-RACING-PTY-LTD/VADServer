//go:build windows

package storage

import "golang.org/x/sys/windows"

func diskSpace(path string) (Space, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Space{}, err
	}
	var avail, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(p, &avail, &total, &free); err != nil {
		return Space{}, err
	}
	return Space{Total: int64(total), Free: int64(free), Available: int64(avail)}, nil
}
