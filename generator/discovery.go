package generator

import (
	yaml "go.yaml.in/yaml/v3"
)

// gnostic's Discovery Document model (github.com/google/gnostic/discovery)
// validates each object against a fixed set of allowed keys and rejects the
// whole document if it encounters an unknown one. Google's live discovery
// documents have since grown fields gnostic's 2019-era schema never learned
// about (notably "deprecated" and "enumDeprecated" on schemas, parameters, and
// methods). These carry no information the proto conversion needs, so we strip
// every key gnostic doesn't recognize — structurally, per object type — before
// handing the document to gnostic's parser.
//
// The key sets below mirror the allowedKeys lists in gnostic's discovery.go
// exactly. Schema and Parameter objects are structurally identical except that
// Schema also permits "readOnly".

var discoveryDocumentKeys = stringSet(
	"auth", "basePath", "baseUrl", "batchPath", "canonicalName", "description",
	"discoveryVersion", "documentationLink", "etag", "features",
	"fullyEncodeReservedExpansion", "icons", "id", "kind", "labels", "methods",
	"mtlsRootUrl", "name", "ownerDomain", "ownerName", "packagePath",
	"parameters", "protocol", "resources", "revision", "rootUrl", "servicePath",
	"title", "version", "version_module",
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
// mapping node, e.g. documentRoot(root)) and removes every key gnostic's
// discovery model would reject. Key order is preserved so downstream proto
// field numbering is stable.
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

	// auth.oauth2.scopes: each scope permits only "description".
	if auth := mapValue(doc, "auth"); auth != nil {
		if oauth2 := mapValue(auth, "oauth2"); oauth2 != nil {
			forEachMapValue(mapValue(oauth2, "scopes"), func(v *yaml.Node) {
				filterMapKeys(v, discoveryScopeKeys)
			})
		}
	}
}

// sanitizeDiscoverySchemaLike sanitizes a Schema or Parameter object. topKeys
// selects the allowed-key set for this node (Schema vs Parameter); nested
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
// not in allowed. Order of the surviving pairs is preserved.
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

// forEachMapValue invokes fn on each value node of a mapping node (i.e. each
// entry of a discovery map such as "schemas", "properties", "methods"). It is a
// no-op when m is nil or not a mapping.
func forEachMapValue(m *yaml.Node, fn func(*yaml.Node)) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 1; i < len(m.Content); i += 2 {
		fn(m.Content[i])
	}
}
