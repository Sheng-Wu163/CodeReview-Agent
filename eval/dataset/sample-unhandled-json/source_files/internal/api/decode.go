package api

import "encoding/json"

// DecodeConfig parses the raw config payload.
func DecodeConfig(data []byte) map[string]string {
	var cfg map[string]string
	json.Unmarshal(data, &cfg)
	return cfg
}