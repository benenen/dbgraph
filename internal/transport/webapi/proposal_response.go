package webapi

import (
	"encoding/json"

	"github.com/benenen/dbgraph/internal/relations"
)

const (
	maximumProposalResponseCount = 50
	maximumProposalResponseBytes = 1 << 20
)

func boundedProposalResponse(found []relations.Relation, countLimit int) ([]json.RawMessage, bool, error) {
	emptyEnvelope, err := json.Marshal(map[string]any{
		"success": true,
		"data":    map[string]any{"relations": []json.RawMessage{}, "truncated": false},
		"error":   nil,
	})
	if err != nil {
		return nil, false, err
	}
	usedBytes := len(emptyEnvelope) + 1
	result := make([]json.RawMessage, 0, min(len(found), countLimit))
	for _, relation := range found {
		if len(result) >= countLimit {
			return result, true, nil
		}
		encoded, err := json.Marshal(mapRelation(relation))
		if err != nil {
			return nil, false, err
		}
		separatorBytes := 0
		if len(result) > 0 {
			separatorBytes = 1
		}
		if usedBytes+separatorBytes+len(encoded) > maximumProposalResponseBytes {
			return result, true, nil
		}
		usedBytes += separatorBytes + len(encoded)
		result = append(result, json.RawMessage(encoded))
	}
	return result, false, nil
}
