//go:build linux

package sys

import "syscall"

// diskUsage reports KB used and KB usable (used + available to non-root),
// the same numbers df prints.
func diskUsage(path string) (used, total uint64, err error) {
	var st syscall.Statfs_t
	if err = syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	used = (st.Blocks - st.Bfree) * bs / 1024
	return used, used + st.Bavail*bs/1024, nil
}
