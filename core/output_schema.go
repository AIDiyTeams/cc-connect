package core

import (
	"encoding/json"
	"fmt"
)

// ValidateOutputSchema bounds a trusted runtime document. The agent runtime
// performs full JSON Schema validation; malformed requests must not downgrade
// to unconstrained generation. Keep the document intact, including required,
// enums and nested constraints from the caller's versioned contract.
func ValidateOutputSchema(schema json.RawMessage) error {
	if len(schema) == 0 {
		return nil
	}
	if len(schema) > 128*1024 {
		return fmt.Errorf("output_schema exceeds 128 KiB")
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(schema, &root); err != nil || root == nil {
		return fmt.Errorf("output_schema must be a JSON object")
	}
	var kind string
	if err := json.Unmarshal(root["type"], &kind); err != nil || kind != "object" {
		return fmt.Errorf("output_schema must have root type object")
	}
	return nil
}
