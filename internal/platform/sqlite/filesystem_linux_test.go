//go:build linux

package sqlite

import "testing"

func TestMountInfoRejectsNetworkFilesystemsAtTheLongestMatchingMount(t *testing.T) {
	t.Parallel()

	mountInfo := []byte(`24 20 0:20 / / rw,relatime - overlay overlay rw
25 24 0:21 / /workspace rw,relatime - ext4 /dev/sda rw
26 24 0:22 / /workspace/shared rw,relatime - nfs4 server:/shared rw
`)
	filesystem, err := filesystemTypeForPath("/workspace/shared/db/dbgraph.sqlite", mountInfo)
	if err != nil {
		t.Fatalf("classify mount: %v", err)
	}
	if filesystem != "nfs4" || !isNetworkFilesystem(filesystem) {
		t.Fatalf("filesystem = %q, want rejected nfs4", filesystem)
	}
	local, err := filesystemTypeForPath("/workspace/local/dbgraph.sqlite", mountInfo)
	if err != nil || local != "ext4" || isNetworkFilesystem(local) {
		t.Fatalf("local filesystem = %q, error = %v", local, err)
	}
}

func TestKnownRemoteAndUserspaceNetworkFilesystemsAreRejected(t *testing.T) {
	t.Parallel()

	for _, filesystem := range []string{"nfs", "nfs4", "cifs", "smb3", "ceph", "9p", "fuse.sshfs", "glusterfs"} {
		if !isNetworkFilesystem(filesystem) {
			t.Fatalf("filesystem %q was accepted", filesystem)
		}
	}
	for _, filesystem := range []string{"ext4", "xfs", "btrfs", "tmpfs", "overlay"} {
		if isNetworkFilesystem(filesystem) {
			t.Fatalf("local filesystem %q was rejected", filesystem)
		}
	}
}
