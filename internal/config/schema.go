package config

import (
	"encoding/json"

	"github.com/invopop/jsonschema"
)

// GenerateSchema returns the JSON Schema for ServiceConfig.
func GenerateSchema() ([]byte, error) {
	r := new(jsonschema.Reflector)
	r.ExpandedStruct = true
	r.DoNotReference = true

	schema := r.Reflect(&ServiceConfig{})
	schema.Title = "workerd Service Configuration"
	schema.Description = "Schema for workerd service TOML configuration files."

	// Only name and command are truly required; all other fields have defaults.
	schema.Required = []string{"name", "command"}

	// Clear required from all nested property schemas (except health_check.type
	// which is required when the health_check table is present).
	if schema.Properties != nil {
		for pair := schema.Properties.Oldest(); pair != nil; pair = pair.Next() {
			if pair.Value.Required != nil && len(pair.Value.Required) > 0 {
				if pair.Key == "health_check" {
					// Keep only "type" as required for health_check
					pair.Value.Required = []string{"type"}
				} else {
					pair.Value.Required = nil
				}
			}
		}
	}

	return json.MarshalIndent(schema, "", "  ")
}
