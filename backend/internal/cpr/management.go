package cpr

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed management-endpoints.json
var managementContract []byte

type ManagementEndpoint struct {
	Method    string    `json:"method"`
	Path      string    `json:"path"`
	Kind      string    `json:"kind"`
	Sensitive bool      `json:"sensitive,omitempty"`
	Operation Operation `json:"-"`
}

var managementEndpoints []ManagementEndpoint

func init() {
	var contract struct {
		Endpoints []ManagementEndpoint `json:"endpoints"`
	}
	if json.Unmarshal(managementContract, &contract) != nil {
		panic("invalid CPR management contract")
	}
	for _, endpoint := range contract.Endpoints {
		operation := Operation(endpoint.Path)
		if existing, ok := operationMethods[operation]; ok && existing != endpoint.Method {
			operation = Operation(endpoint.Path + "#" + endpoint.Method)
		}
		operationMethods[operation] = endpoint.Method
		endpoint.Operation = operation
		managementEndpoints = append(managementEndpoints, endpoint)
	}
}

func ManagementEndpoints() []ManagementEndpoint {
	return append([]ManagementEndpoint(nil), managementEndpoints...)
}

func (o Operation) Path() string {
	path, _, _ := strings.Cut(string(o), "#")
	return path
}

func (o Operation) Kind() string {
	for _, endpoint := range managementEndpoints {
		if endpoint.Operation == o {
			return endpoint.Kind
		}
	}
	return "json"
}
