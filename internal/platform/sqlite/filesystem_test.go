package sqlite

import (
	"errors"
	"testing"
)

func TestFilesystemValidationFailsClosedOnUnsupportedPlatforms(t *testing.T) {
	t.Parallel()

	if err := requireFilesystemValidation("linux"); err != nil {
		t.Fatalf("Linux filesystem validation error = %v", err)
	}
	for _, platform := range []string{"darwin", "freebsd", "windows"} {
		if err := requireFilesystemValidation(platform); !errors.Is(err, ErrUnsupportedFilesystemPlatform) {
			t.Fatalf("platform %q error = %v, want %v", platform, err, ErrUnsupportedFilesystemPlatform)
		}
	}
}
