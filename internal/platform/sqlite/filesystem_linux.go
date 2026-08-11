//go:build linux

package sqlite

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func rejectNetworkFilesystem(path string) error {
	mountInfo, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return fmt.Errorf("inspect SQLite filesystem: %w", err)
	}
	filesystem, err := filesystemTypeForPath(path, mountInfo)
	if err != nil {
		return fmt.Errorf("inspect SQLite filesystem: %w", err)
	}
	if isNetworkFilesystem(filesystem) {
		return fmt.Errorf("%w: %s", ErrNetworkFilesystem, filesystem)
	}
	return nil
}

func filesystemTypeForPath(path string, mountInfo []byte) (string, error) {
	path = filepath.Clean(path)
	bestMount := ""
	bestFilesystem := ""
	for _, line := range strings.Split(string(mountInfo), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 0 || separator+1 >= len(fields) {
			continue
		}
		mountPoint := unescapeMountInfo(fields[4])
		if !pathWithinMount(path, mountPoint) || len(mountPoint) <= len(bestMount) {
			continue
		}
		bestMount = mountPoint
		bestFilesystem = fields[separator+1]
	}
	if bestFilesystem == "" {
		return "", errors.New("no mount contains SQLite path")
	}
	return strings.ToLower(bestFilesystem), nil
}

func pathWithinMount(path string, mountPoint string) bool {
	relative, err := filepath.Rel(mountPoint, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func unescapeMountInfo(value string) string {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(value)
}

func isNetworkFilesystem(filesystem string) bool {
	filesystem = strings.ToLower(strings.TrimSpace(filesystem))
	if strings.HasPrefix(filesystem, "fuse.") {
		return true
	}
	switch filesystem {
	case "9p", "afs", "ceph", "cifs", "coda", "glusterfs", "lustre", "ncp", "nfs", "nfs4", "orangefs", "pvfs2", "smb2", "smb3":
		return true
	default:
		return false
	}
}
