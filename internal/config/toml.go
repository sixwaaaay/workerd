package config

import "github.com/BurntSushi/toml"

func unmarshalTOML(data []byte, v interface{}) error {
	return toml.Unmarshal(data, v)
}
