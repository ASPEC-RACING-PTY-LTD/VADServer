package media

import "golang.org/x/sys/unix"

func setNice(pid, nice int) { _ = unix.Setpriority(unix.PRIO_PROCESS, pid, nice) }
