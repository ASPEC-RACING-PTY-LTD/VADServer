//go:build linux || darwin

package nodeagent

import "golang.org/x/sys/unix"

// diskSpace returns the total and free bytes of the filesystem holding path.
func diskSpace(path string) (total, free int64) {
	var st unix.Statfs_t
	if unix.Statfs(path, &st) != nil {
		return 0, 0
	}
	return int64(st.Blocks) * int64(st.Bsize), int64(st.Bavail) * int64(st.Bsize)
}
