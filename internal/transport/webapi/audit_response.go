package webapi

import (
	"encoding/json"

	"github.com/benenen/dbgraph/internal/audit"
)

const (
	maximumAuditResponseCount = 50
	maximumAuditResponseBytes = 1 << 20
)

func boundedAuditResponse(found []audit.Event, countLimit int) ([]json.RawMessage, bool, error) {
	result := make([]json.RawMessage, 0, min(len(found), countLimit))
	completeBaseBytes, err := auditEnvelopeBytes([]json.RawMessage{}, false)
	if err != nil {
		return nil, false, err
	}
	truncatedBaseBytes, err := auditEnvelopeBytes([]json.RawMessage{}, true)
	if err != nil {
		return nil, false, err
	}
	itemsBytes := 0
	for _, event := range found {
		if len(result) >= countLimit {
			return result, true, nil
		}
		encoded, err := json.Marshal(mapAuditEvent(event))
		if err != nil {
			return nil, false, err
		}
		candidateItemsBytes := itemsBytes + len(encoded)
		if len(result) > 0 {
			candidateItemsBytes++
		}
		truncated := len(found) > len(result)+1
		baseBytes := completeBaseBytes
		if truncated {
			baseBytes = truncatedBaseBytes
		}
		if baseBytes+candidateItemsBytes > maximumAuditResponseBytes {
			return result, true, nil
		}
		result = append(result, json.RawMessage(encoded))
		itemsBytes = candidateItemsBytes
	}
	return result, false, nil
}

func auditEnvelopeBytes(events []json.RawMessage, truncated bool) (int, error) {
	encoded, err := json.Marshal(map[string]any{
		"success": true,
		"data":    map[string]any{"events": events, "truncated": truncated},
		"error":   nil,
	})
	if err != nil {
		return 0, err
	}
	return len(encoded) + 1, nil
}
