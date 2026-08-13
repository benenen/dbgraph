package mcpapi

func inputSchema(name string) map[string]any {
	switch name {
	case "dbgraph_status":
		return strictObject(nil, nil)
	case "dbgraph_search_nodes":
		return strictObject(map[string]any{
			"query": stringSchema(1, 500), "limit": integerSchema(1, 100),
		}, []string{"query"})
	case "dbgraph_get_node":
		return strictObject(map[string]any{
			"dataSourceId": idSchema(), "qualifiedName": stringSchema(1, 1000),
		}, []string{"dataSourceId", "qualifiedName"})
	case "dbgraph_get_relation", "dbgraph_explain_relation":
		return strictObject(map[string]any{"relationId": idSchema()}, []string{"relationId"})
	case "dbgraph_list_proposals":
		return strictObject(map[string]any{"limit": integerSchema(1, 100)}, nil)
	case "dbgraph_trace", "dbgraph_impact":
		return traceSchema()
	case "dbgraph_propose_relation":
		return relationCreateSchema()
	case "dbgraph_propose_relation_revision":
		return relationRevisionSchema()
	case "dbgraph_propose_relation_tombstone", "dbgraph_suppress_relation", "dbgraph_restore_relation":
		return relationStateSchema()
	case "dbgraph_review_relation":
		return strictObject(map[string]any{
			"relationId": idSchema(), "expectedRevisionNo": integerSchema(1, 1_000_000),
			"decision": enumSchema("APPROVE", "REJECT"), "reason": stringSchema(1, 2000),
			"requestId": stringSchema(1, 200),
		}, []string{"relationId", "expectedRevisionNo", "decision", "reason", "requestId"})
	case "dbgraph_begin_relation_init":
		return relationInitBeginSchema()
	case "dbgraph_propose_relations":
		return relationBatchSchema()
	case "dbgraph_complete_relation_init":
		return strictObject(map[string]any{
			"sessionId": idSchema(), "expectedBatchCount": integerSchema(1, 1_000_000),
			"reason": stringSchema(1, 2000), "requestId": stringSchema(1, 180),
		}, []string{"sessionId", "expectedBatchCount", "reason", "requestId"})
	case "dbgraph_get_relation_init":
		return strictObject(map[string]any{"sessionId": idSchema()}, []string{"sessionId"})
	case "dbgraph_list_unresolved":
		return strictObject(map[string]any{"limit": integerSchema(1, 100)}, nil)
	case "dbgraph_start_schema_scan":
		return strictObject(map[string]any{
			"dataSourceId": idSchema(),
			"mode":         enumSchema("FULL", "INCREMENTAL"),
			"tables": map[string]any{
				"type": "array", "maxItems": 100,
				"items": stringSchema(3, 1000),
			},
			"reason": stringSchema(1, 2000), "requestId": stringSchema(1, 200),
		}, []string{"dataSourceId", "reason", "requestId"})
	case "dbgraph_get_job":
		return strictObject(map[string]any{"jobId": idSchema()}, []string{"jobId"})
	default:
		return strictObject(nil, nil)
	}
}

func relationInitBeginSchema() map[string]any {
	schema := strictObject(map[string]any{
		"repositoryId": idSchema(), "mode": enumSchema("FULL", "INCREMENTAL"),
		"sourceCommit": stringSchema(1, 200), "scope": map[string]any{"type": "object"},
		"requestId": stringSchema(1, 200),
	}, []string{"repositoryId", "mode", "sourceCommit", "requestId"})
	incrementalScope := strictObject(map[string]any{
		"relationIds": map[string]any{
			"type": "array", "maxItems": 1000, "uniqueItems": true, "items": idSchema(),
		},
	}, []string{"relationIds"})
	schema["allOf"] = []any{map[string]any{
		"if": map[string]any{
			"properties": map[string]any{"mode": map[string]any{"const": "INCREMENTAL"}},
			"required":   []string{"mode"},
		},
		"then": map[string]any{
			"properties": map[string]any{"scope": incrementalScope},
			"required":   []string{"scope"},
		},
	}}
	return schema
}

func traceSchema() map[string]any {
	return strictObject(map[string]any{
		"startNodeId": idSchema(), "targetNodeId": idSchema(),
		"direction": enumSchema("UPSTREAM", "DOWNSTREAM"),
		"context": strictObject(map[string]any{
			"columns":    map[string]any{"type": "object", "maxProperties": 1000},
			"parameters": map[string]any{"type": "object", "maxProperties": 1000},
		}, nil),
		"maxDepth": integerSchema(1, 64), "maxNodes": integerSchema(1, 10_000),
		"maxPaths": integerSchema(1, 10_000),
	}, []string{"startNodeId"})
}

func relationCreateSchema() map[string]any {
	properties := relationContentProperties()
	properties["type"] = enumSchema("CONDITIONAL_VALUE_COPY")
	properties["reason"] = stringSchema(1, 2000)
	properties["requestId"] = stringSchema(1, 200)
	root := strictObject(properties, []string{
		"type", "sourceNodeId", "targetNodeId", "transform", "confidence", "evidence", "reason", "requestId",
	})
	root["$defs"] = conditionDefinitions()
	return root
}

func relationRevisionSchema() map[string]any {
	properties := relationContentProperties()
	properties["relationId"] = idSchema()
	properties["expectedRevisionNo"] = integerSchema(1, 1_000_000)
	properties["reason"] = stringSchema(1, 2000)
	properties["requestId"] = stringSchema(1, 200)
	root := strictObject(properties, []string{
		"relationId", "expectedRevisionNo", "sourceNodeId", "targetNodeId", "transform", "confidence", "evidence", "reason", "requestId",
	})
	root["$defs"] = conditionDefinitions()
	return root
}

func relationStateSchema() map[string]any {
	return strictObject(map[string]any{
		"relationId": idSchema(), "expectedRevisionNo": integerSchema(1, 1_000_000),
		"reason": stringSchema(1, 2000), "requestId": stringSchema(1, 200),
	}, []string{"relationId", "expectedRevisionNo", "reason", "requestId"})
}

func relationBatchSchema() map[string]any {
	proposalProperties := relationContentProperties()
	proposalProperties["type"] = enumSchema("CONDITIONAL_VALUE_COPY")
	proposalProperties["reason"] = stringSchema(1, 2000)
	proposal := strictObject(proposalProperties, []string{
		"type", "sourceNodeId", "targetNodeId", "transform", "confidence", "evidence", "reason",
	})
	unresolved := strictObject(map[string]any{
		"type": stringSchema(1, 100), "summary": stringSchema(1, 2000), "evidence": map[string]any{},
	}, []string{"type", "summary", "evidence"})
	root := strictObject(map[string]any{
		"sessionId": idSchema(), "batchNo": integerSchema(1, 1_000_000),
		"idempotencyKey": stringSchema(1, 200),
		"proposals":      map[string]any{"type": "array", "maxItems": 100, "items": proposal},
		"unresolved":     map[string]any{"type": "array", "maxItems": 100, "items": unresolved},
		"requestId":      stringSchema(1, 180),
	}, []string{"sessionId", "batchNo", "idempotencyKey", "requestId"})
	root["$defs"] = conditionDefinitions()
	return root
}

func relationContentProperties() map[string]any {
	return map[string]any{
		"sourceNodeId": idSchema(), "targetNodeId": idSchema(),
		"guard":      map[string]any{"$ref": "#/$defs/boolean"},
		"selector":   map[string]any{"$ref": "#/$defs/boolean"},
		"transform":  map[string]any{"$ref": "#/$defs/value"},
		"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		"evidence": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 20,
			"items": strictObject(map[string]any{
				"kind": enumSchema("CODE", "SQL_MAPPING", "MANUAL"), "repository": stringSchema(1, 500),
				"commit": stringSchema(1, 200), "file": stringSchema(1, 2000), "symbol": stringSchema(0, 2000),
				"startLine": integerSchema(1, 2_000_000_000), "endLine": integerSchema(1, 2_000_000_000),
			}, []string{"kind", "repository", "commit", "file", "startLine", "endLine"}),
		},
	}
}

func conditionDefinitions() map[string]any {
	value := strictObject(map[string]any{
		"kind":   enumSchema("column", "literal", "parameter", "column_copy", "case"),
		"nodeId": idSchema(), "parameter": stringSchema(1, 2000),
		"valueType": enumSchema("integer", "decimal", "string", "boolean", "null"),
		"value":     map[string]any{},
		"literal": strictObject(map[string]any{
			"type": enumSchema("integer", "decimal", "string", "boolean", "null"), "value": map[string]any{},
		}, []string{"type", "value"}),
		"cases": map[string]any{
			"type": "array", "maxItems": 100,
			"items": strictObject(map[string]any{
				"when": map[string]any{"$ref": "#/$defs/boolean"}, "then": map[string]any{"$ref": "#/$defs/value"},
			}, []string{"when", "then"}),
		},
		"else": map[string]any{"$ref": "#/$defs/value"},
	}, []string{"kind"})
	boolean := strictObject(map[string]any{
		"kind":     enumSchema("and", "or", "not", "compare", "in", "not_in", "is_null", "is_not_null"),
		"operator": enumSchema("eq", "ne", "gt", "gte", "lt", "lte"),
		"children": map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"$ref": "#/$defs/boolean"}},
		"operand":  map[string]any{"$ref": "#/$defs/boolean"},
		"left":     map[string]any{"$ref": "#/$defs/value"}, "right": map[string]any{"$ref": "#/$defs/value"},
		"values": map[string]any{"type": "array", "maxItems": 100, "items": map[string]any{"$ref": "#/$defs/value"}},
	}, []string{"kind"})
	return map[string]any{"boolean": boolean, "value": value}
}

func strictObject(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	result := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		result["required"] = required
	}
	return result
}

func idSchema() map[string]any {
	return map[string]any{"type": "string", "pattern": "^[1-9][0-9]{0,18}$"}
}

func stringSchema(minimum int, maximum int) map[string]any {
	return map[string]any{"type": "string", "minLength": minimum, "maxLength": maximum}
}

func integerSchema(minimum int, maximum int) map[string]any {
	return map[string]any{"type": "integer", "minimum": minimum, "maximum": maximum}
}

func enumSchema(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
