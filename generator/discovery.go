package generator

import (
	"encoding/json"
	"fmt"

	openapiv3 "github.com/accretional/openapi2proto/internal/openapiv3"
	discovery "github.com/google/gnostic/discovery"
	yaml "go.yaml.in/yaml/v3"
)

// This file adds support for Google API Discovery Documents (kind:
// discovery#restDescription), which the Google APIs discovery service serves at
// each API's "discoveryRestUrl". They are not OpenAPI, so they are converted to
// the internal OpenAPI v3 model via a vendored port of gnostic's discovery
// converter (see discovery_convert.go), which targets internal/openapiv3
// directly — avoiding a dependency on gnostic-models/openapiv3, whose proto
// descriptors would collide with internal/openapiv3 at registration time.

// looksLikeDiscovery reports whether the raw bytes are a Google API Discovery
// Document rather than an OpenAPI/Swagger spec. Discovery documents carry no
// "openapi"/"swagger" version key and identify themselves with
// "kind": "discovery#restDescription" and/or a "discoveryVersion" field.
func looksLikeDiscovery(data []byte) bool {
	var probe struct {
		Kind             string `json:"kind"`
		DiscoveryVersion string `json:"discoveryVersion"`
		OpenAPI          string `json:"openapi"`
		Swagger          string `json:"swagger"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	if probe.OpenAPI != "" || probe.Swagger != "" {
		return false
	}
	return probe.Kind == "discovery#restDescription" || probe.DiscoveryVersion != ""
}

// discoveryToOpenAPIv3 converts a Google Discovery Document to the internal
// OpenAPI v3 model. The document is first stripped of keys gnostic's strict
// discovery model would reject (see sanitizeDiscoveryDocument), converted to
// gnostic's OpenAPI v3 Document, then bridged to the internal openapiv3 model
// over the protobuf wire format.
func discoveryToOpenAPIv3(data []byte) (*openapiv3.Document, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	sanitizeDiscoveryDocument(documentRoot(&root))
	normalized, err := yaml.Marshal(&root)
	if err != nil {
		return nil, err
	}

	doc, err := discovery.ParseDocument(normalized)
	if err != nil {
		return nil, fmt.Errorf("parsing discovery document: %w", err)
	}
	v3, err := OpenAPIv3(doc)
	if err != nil {
		return nil, fmt.Errorf("converting discovery document to OpenAPI v3: %w", err)
	}
	return v3, nil
}

// --- yaml.Node helpers (kept local to the discovery path) ---

func documentRoot(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		return n.Content[0]
	}
	return n
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// --- discovery sanitizer ---
//
// gnostic's Discovery Document model validates each object against a fixed set
// of allowed keys and rejects the whole document on an unknown one. Google's
// live discovery documents have since grown fields gnostic's schema never
// learned about (notably "deprecated" and "enumDeprecated"). These carry no
// information the proto conversion needs, so every unrecognized key is stripped
// — structurally, per object type — before handing the document to gnostic.
//
// The key sets below mirror the allowedKeys lists in gnostic's discovery.go.
// Schema and Parameter objects are structurally identical except that Schema
// also permits "readOnly".

var discoveryDocumentKeys = stringSet(
	"auth", "basePath", "baseUrl", "batchPath", "canonicalName", "description",
	"discoveryVersion", "documentationLink", "etag", "features",
	"fullyEncodeReservedExpansion", "icons", "id", "kind", "labels", "methods",
	"mtlsRootUrl", "name", "ownerDomain", "ownerName", "packagePath",
	"parameters", "protocol", "resources", "revision", "rootUrl", "schemas",
	"servicePath", "title", "version", "version_module",
)

var discoverySchemaKeys = stringSet(
	"$ref", "additionalProperties", "annotations", "default", "description",
	"enum", "enumDescriptions", "format", "id", "items", "location", "maximum",
	"minimum", "pattern", "properties", "readOnly", "repeated", "required",
	"type",
)

var discoveryParameterKeys = stringSet(
	"$ref", "additionalProperties", "annotations", "default", "description",
	"enum", "enumDescriptions", "format", "id", "items", "location", "maximum",
	"minimum", "pattern", "properties", "repeated", "required", "type",
)

var discoveryMethodKeys = stringSet(
	"description", "etagRequired", "flatPath", "httpMethod", "id", "mediaUpload",
	"parameterOrder", "parameters", "path", "request", "response", "scopes",
	"streamingType", "supportsMediaDownload", "supportsMediaUpload",
	"supportsSubscription", "useMediaDownloadService",
)

var discoveryResourceKeys = stringSet("methods", "resources")
var discoveryAnnotationKeys = stringSet("required")
var discoveryRequestKeys = stringSet("$ref", "parameterName")
var discoveryResponseKeys = stringSet("$ref")
var discoveryMediaUploadKeys = stringSet("accept", "maxSize", "protocols", "supportsSubscription")
var discoveryScopeKeys = stringSet("description")

func stringSet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, k := range keys {
		m[k] = true
	}
	return m
}

// sanitizeDiscoveryDocument walks a parsed Google Discovery Document (the
// mapping node) and removes every key gnostic's discovery model would reject.
// Key order is preserved so downstream proto field numbering is stable.
func sanitizeDiscoveryDocument(doc *yaml.Node) {
	if doc == nil || doc.Kind != yaml.MappingNode {
		return
	}
	filterMapKeys(doc, discoveryDocumentKeys)

	forEachMapValue(mapValue(doc, "schemas"), func(v *yaml.Node) {
		sanitizeDiscoverySchemaLike(v, discoverySchemaKeys)
	})
	forEachMapValue(mapValue(doc, "parameters"), func(v *yaml.Node) {
		sanitizeDiscoverySchemaLike(v, discoveryParameterKeys)
	})
	forEachMapValue(mapValue(doc, "methods"), sanitizeDiscoveryMethod)
	forEachMapValue(mapValue(doc, "resources"), sanitizeDiscoveryResource)

	if auth := mapValue(doc, "auth"); auth != nil {
		if oauth2 := mapValue(auth, "oauth2"); oauth2 != nil {
			forEachMapValue(mapValue(oauth2, "scopes"), func(v *yaml.Node) {
				filterMapKeys(v, discoveryScopeKeys)
			})
		}
	}
}

// sanitizeDiscoverySchemaLike sanitizes a Schema or Parameter object. topKeys
// selects the allowed-key set for this node; nested
// properties/items/additionalProperties are always Schemas.
func sanitizeDiscoverySchemaLike(node *yaml.Node, topKeys map[string]bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	filterMapKeys(node, topKeys)
	forEachMapValue(mapValue(node, "properties"), func(v *yaml.Node) {
		sanitizeDiscoverySchemaLike(v, discoverySchemaKeys)
	})
	sanitizeDiscoverySchemaLike(mapValue(node, "additionalProperties"), discoverySchemaKeys)
	sanitizeDiscoverySchemaLike(mapValue(node, "items"), discoverySchemaKeys)
	if an := mapValue(node, "annotations"); an != nil {
		filterMapKeys(an, discoveryAnnotationKeys)
	}
}

func sanitizeDiscoveryMethod(node *yaml.Node) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	filterMapKeys(node, discoveryMethodKeys)
	forEachMapValue(mapValue(node, "parameters"), func(v *yaml.Node) {
		sanitizeDiscoverySchemaLike(v, discoveryParameterKeys)
	})
	if req := mapValue(node, "request"); req != nil {
		filterMapKeys(req, discoveryRequestKeys)
	}
	if resp := mapValue(node, "response"); resp != nil {
		filterMapKeys(resp, discoveryResponseKeys)
	}
	if mu := mapValue(node, "mediaUpload"); mu != nil {
		filterMapKeys(mu, discoveryMediaUploadKeys)
	}
}

func sanitizeDiscoveryResource(node *yaml.Node) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	filterMapKeys(node, discoveryResourceKeys)
	forEachMapValue(mapValue(node, "methods"), sanitizeDiscoveryMethod)
	forEachMapValue(mapValue(node, "resources"), sanitizeDiscoveryResource)
}

// filterMapKeys removes from a mapping node every key/value pair whose key is
// not in allowed, preserving the order of survivors.
func filterMapKeys(node *yaml.Node, allowed map[string]bool) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	out := make([]*yaml.Node, 0, len(node.Content))
	for i := 0; i+1 < len(node.Content); i += 2 {
		if allowed[node.Content[i].Value] {
			out = append(out, node.Content[i], node.Content[i+1])
		}
	}
	node.Content = out
}

// forEachMapValue invokes fn on each value node of a mapping node. It is a
// no-op when m is nil or not a mapping.
func forEachMapValue(m *yaml.Node, fn func(*yaml.Node)) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 1; i < len(m.Content); i += 2 {
		fn(m.Content[i])
	}
}
