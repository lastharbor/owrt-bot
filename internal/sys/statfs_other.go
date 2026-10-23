//go:build !linux

package sys

import "errors"

func diskUsage(string) (used, total uint64, err error) {
	return 0, 0, errors.New("statfs: unsupported OS")
}
