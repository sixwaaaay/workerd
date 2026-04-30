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

	return json.MarshalIndent(schema, "", "  ")
}
