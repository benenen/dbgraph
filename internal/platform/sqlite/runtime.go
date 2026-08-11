package sqlite

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const MinimumRuntimeVersion = "3.51.3"

var ErrUnsupportedRuntime = errors.New("unsupported SQLite runtime")

func CheckRuntimeVersion(version string) error {
	actual, err := parseRuntimeVersion(version)
	if err != nil {
		return fmt.Errorf("%w: %q is not a semantic version", ErrUnsupportedRuntime, version)
	}
	minimum, _ := parseRuntimeVersion(MinimumRuntimeVersion)

	for index := range actual {
		if actual[index] > minimum[index] {
			return nil
		}
		if actual[index] < minimum[index] {
			return fmt.Errorf("%w: have %s, require at least %s", ErrUnsupportedRuntime, version, MinimumRuntimeVersion)
		}
	}

	return nil
}

func parseRuntimeVersion(version string) ([3]int, error) {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return [3]int{}, errors.New("version must have three parts")
	}

	var parsed [3]int
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return [3]int{}, errors.New("version contains an invalid part")
		}
		parsed[index] = value
	}
	return parsed, nil
}
