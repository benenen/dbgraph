//go:build linux

package sqlite

import (
	"math"
	"testing"
)

func TestCheckedFileDescriptorConversionsRejectInvalidAndOverflowValues(t *testing.T) {
	if _, err := checkedFileDescriptorValue(^uintptr(0)); err == nil {
		t.Fatal("checkedFileDescriptorValue accepted an overflowing uintptr")
	}
	if _, err := checkedDescriptorValue(-1); err == nil {
		t.Fatal("checkedDescriptorValue accepted a negative descriptor")
	}

	descriptor, err := checkedFileDescriptorValue(17)
	if err != nil || descriptor != 17 {
		t.Fatalf("checkedFileDescriptorValue(17) = %d, %v", descriptor, err)
	}
	value, err := checkedDescriptorValue(17)
	if err != nil || value != 17 {
		t.Fatalf("checkedDescriptorValue(17) = %d, %v", value, err)
	}
}

func TestCheckedUserIDRejectsInvalidAndOverflowValues(t *testing.T) {
	for _, value := range []int64{-1, int64(math.MaxUint32) + 1} {
		if _, err := checkedUserIDValue(value); err == nil {
			t.Fatalf("checkedUserIDValue(%d) accepted an invalid user ID", value)
		}
	}

	userID, err := checkedUserIDValue(math.MaxUint32)
	if err != nil || userID != math.MaxUint32 {
		t.Fatalf("checkedUserIDValue(MaxUint32) = %d, %v", userID, err)
	}
}
