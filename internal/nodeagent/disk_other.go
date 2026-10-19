//go:build !linux && !darwin

package nodeagent

func diskSpace(string) (total, free int64) { return 0, 0 }
