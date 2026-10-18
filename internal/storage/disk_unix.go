//go:build !windows

package storage

import "syscall"

func diskSpace(path string) (Space, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Space{}, err
	}
	bs := int64(st.Bsize)
	return Space{Total: int64(st.Blocks) * bs, Free: int64(st.Bfree) * bs, Available: int64(st.Bavail) * bs}, nil
}
