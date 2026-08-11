//go:build !linux

package sqlite

import "os"

func newBackupStage(*os.Root) (backupStage, error) {
	return nil, ErrUnsupportedFilesystemPlatform
}
