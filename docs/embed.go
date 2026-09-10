package docs

import _ "embed"

// OpenAPI is the control-plane HTTP spec (YAML).
//
//go:embed openapi.yaml
var OpenAPI []byte

// LLMs is the short agent-oriented reference.
//
//go:embed llms.txt
var LLMs []byte
