//go:build linux

package sqlite

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"

	"golang.org/x/sys/unix"
)

const backupTemporaryFileName = "snapshot.sqlite"

type linuxBackupStage struct {
	destinationDirectory *os.File
	temporaryDirectory   *os.File
	backupFile           *os.File
	destinationFD        int
	temporaryFD          int
	temporaryName        string
	path                 string
}

func newBackupStage(root *os.Root) (backupStage, error) {
	destinationDirectory, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open pinned backup destination directory: %w", err)
	}
	destinationFD, err := checkedFileDescriptor(destinationDirectory)
	if err != nil {
		return nil, errors.Join(err, destinationDirectory.Close())
	}
	stage := &linuxBackupStage{
		destinationDirectory: destinationDirectory,
		destinationFD:        destinationFD,
		temporaryFD:          -1,
	}
	temporaryName, err := createBackupTemporaryDirectory(root)
	if err != nil {
		return nil, errors.Join(err, stage.Close())
	}
	stage.temporaryName = temporaryName

	temporaryFD, err := unix.Openat(
		destinationFD,
		temporaryName,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open pinned backup temporary directory: %w", err), stage.Close())
	}
	stage.temporaryFD = temporaryFD
	stage.temporaryDirectory, err = fileFromDescriptor(temporaryFD, temporaryName)
	if err != nil {
		return nil, errors.Join(err, unix.Close(temporaryFD), stage.Close())
	}
	effectiveUserID, err := checkedUserIDValue(int64(os.Geteuid()))
	if err != nil {
		return nil, errors.Join(err, stage.Close())
	}
	if err := validateBackupTemporaryDirectory(temporaryFD, effectiveUserID); err != nil {
		return nil, errors.Join(err, stage.Close())
	}

	backupFD, err := unix.Openat(
		temporaryFD,
		backupTemporaryFileName,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("create pinned SQLite backup: %w", err), stage.Close())
	}
	stage.backupFile, err = fileFromDescriptor(backupFD, backupTemporaryFileName)
	if err != nil {
		return nil, errors.Join(err, unix.Close(backupFD), stage.Close())
	}
	stage.path = fmt.Sprintf("/proc/self/fd/%d/%s", temporaryFD, backupTemporaryFileName)
	return stage, nil
}

func validateBackupTemporaryDirectory(fileDescriptor int, effectiveUserID uint32) error {
	var status unix.Stat_t
	if err := unix.Fstat(fileDescriptor, &status); err != nil {
		return fmt.Errorf("inspect backup temporary directory: %w", err)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFDIR || status.Mode&0o777 != 0o700 || status.Uid != effectiveUserID {
		return errors.New("backup temporary directory ownership or permissions changed")
	}
	return nil
}

func checkedFileDescriptor(file *os.File) (int, error) {
	if file == nil {
		return 0, errors.New("file descriptor source is nil")
	}
	return checkedFileDescriptorValue(file.Fd())
}

func checkedFileDescriptorValue(value uintptr) (int, error) {
	if value > uintptr(math.MaxInt) {
		return 0, errors.New("file descriptor exceeds the platform int range")
	}
	// #nosec G115 -- value is proven no greater than math.MaxInt above.
	return int(value), nil
}

func checkedDescriptorValue(value int) (uintptr, error) {
	if value < 0 {
		return 0, errors.New("file descriptor is negative")
	}
	// #nosec G115 -- a non-negative int always fits the same-width uintptr.
	return uintptr(value), nil
}

func fileFromDescriptor(value int, name string) (*os.File, error) {
	fileDescriptor, err := checkedDescriptorValue(value)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(fileDescriptor, name)
	if file == nil {
		return nil, errors.New("create file from descriptor")
	}
	return file, nil
}

func checkedUserIDValue(value int64) (uint32, error) {
	if value < 0 || value > int64(math.MaxUint32) {
		return 0, errors.New("effective user ID is outside the uint32 range")
	}
	// #nosec G115 -- value is proven within the uint32 range above.
	return uint32(value), nil
}

func (s *linuxBackupStage) Path() string {
	return s.path
}

func (s *linuxBackupStage) File() *os.File {
	return s.backupFile
}

func (s *linuxBackupStage) Publish(destinationName string) error {
	if err := unix.Linkat(
		s.temporaryFD,
		backupTemporaryFileName,
		s.destinationFD,
		destinationName,
		0,
	); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrBackupDestinationExists
		}
		return fmt.Errorf("publish SQLite backup: %w", err)
	}
	return nil
}

func (s *linuxBackupStage) SyncDestination() error {
	if err := s.destinationDirectory.Sync(); err != nil {
		return fmt.Errorf("synchronize SQLite backup directory: %w", err)
	}
	return nil
}

func (s *linuxBackupStage) Close() error {
	var result error
	if s.backupFile != nil {
		result = errors.Join(result, s.backupFile.Close())
		s.backupFile = nil
	}
	if s.temporaryDirectory != nil {
		for _, name := range []string{
			backupTemporaryFileName,
			backupTemporaryFileName + "-journal",
			backupTemporaryFileName + "-wal",
			backupTemporaryFileName + "-shm",
		} {
			if err := unix.Unlinkat(
				s.temporaryFD,
				name,
				0,
			); err != nil && !errors.Is(err, fs.ErrNotExist) {
				result = errors.Join(result, fmt.Errorf("remove SQLite backup staging artifact: %w", err))
			}
		}
		result = errors.Join(result, s.temporaryDirectory.Close())
		s.temporaryDirectory = nil
		s.temporaryFD = -1
	}
	if s.destinationDirectory != nil {
		if s.temporaryName != "" {
			if err := unix.Unlinkat(
				s.destinationFD,
				s.temporaryName,
				unix.AT_REMOVEDIR,
			); err != nil && !errors.Is(err, fs.ErrNotExist) {
				result = errors.Join(result, fmt.Errorf("remove backup temporary directory: %w", err))
			}
		}
		result = errors.Join(result, s.destinationDirectory.Close())
		s.destinationDirectory = nil
		s.destinationFD = -1
	}
	return result
}
