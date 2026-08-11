package reconcile

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
)

const maximumIncrementalRelationIDs = 1000

type incrementalScope struct {
	RelationIDs *[]string `json:"relationIds"`
}

// IncrementalRelationIDs validates and parses the explicit relation scope used by incremental sessions.
func IncrementalRelationIDs(scope json.RawMessage) ([]int64, error) {
	decoder := json.NewDecoder(bytes.NewReader(scope))
	decoder.DisallowUnknownFields()
	var input incrementalScope
	if err := decoder.Decode(&input); err != nil || input.RelationIDs == nil {
		return nil, ErrInvalidInit
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, ErrInvalidInit
	}
	if len(*input.RelationIDs) > maximumIncrementalRelationIDs {
		return nil, ErrInvalidInit
	}

	ids := make([]int64, 0, len(*input.RelationIDs))
	seen := make(map[int64]struct{}, len(*input.RelationIDs))
	for _, value := range *input.RelationIDs {
		if !isDecimalID(value) {
			return nil, ErrInvalidInit
		}
		id, err := strconv.ParseInt(value, 10, 64)
		if err != nil || id <= 0 {
			return nil, ErrInvalidInit
		}
		if _, exists := seen[id]; exists {
			return nil, ErrInvalidInit
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}

func isDecimalID(value string) bool {
	if len(value) < 1 || len(value) > 19 || value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
