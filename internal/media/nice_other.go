//go:build !linux

package media

func setNice(pid, nice int) {}
