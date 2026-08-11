package sqlite

import (
	"errors"
	"fmt"
)

var (
	ErrNetworkFilesystem             = errors.New("SQLite WAL database cannot use a network filesystem")
	ErrUnsupportedFilesystemPlatform = errors.New("SQLite filesystem validation is supported only on Linux")
)

func requireFilesystemValidation(platform string) error {
	if platform != "linux" {
		return fmt.Errorf("%w: %s", ErrUnsupportedFilesystemPlatform, platform)
	}
	return nil
}
