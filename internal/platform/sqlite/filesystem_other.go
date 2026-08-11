//go:build !linux

package sqlite

import (
	"fmt"
	"runtime"
)

func rejectNetworkFilesystem(path string) error {
	return fmt.Errorf("%w: %s", ErrUnsupportedFilesystemPlatform, runtime.GOOS)
}
