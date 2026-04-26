package scpcore

//go:generate wget https://www.servercontrolpanel.de/scp-core/api/v1/openapi -O api.yaml
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -config cfg.yaml api.yaml
