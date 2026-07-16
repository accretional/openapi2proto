package generator

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	yaml "go.yaml.in/yaml/v3"

	openapiv3 "github.com/accretional/openapi2proto/internal/openapiv3"
)

type Config struct {
	PackageName         string
	GoPackage           string
	ServiceGrouping     string
	EmitHTTPAnnotations bool
	HasHTTPPreference   bool
	// GoModule is the Go module path of the consuming project (e.g.
	// "github.com/accretional/proto-cloudflare"). Required for service generation.
	GoModule string
	// PbSubPath is the import sub-path inside GoModule where protoc writes pb files
	// (e.g. "pb/cloudflare/zones"). Derived from --go_package if not set.
	PbSubPath string
}

type generator struct {
	cfg              Config
	sourceName       string
	doc              *openapiv3.Document
	refs             *refResolver
	imports          map[string]bool
	services         map[string]*serviceDef
	serviceOrder     []string
	serviceNames     map[string]int
	messageNames     map[string]int
	componentNames   map[string]string
	messages         map[string]*messageDef
	messageOrder     []string
	processingSchema map[string]bool // recursion guard for protoTypeForSchema
}

type refResolver struct {
	schemas       map[string]*openapiv3.SchemaOrReference
	parameters    map[string]*openapiv3.ParameterOrReference
	responses     map[string]*openapiv3.ResponseOrReference
	requestBodies map[string]*openapiv3.RequestBodyOrReference
	headers       map[string]*openapiv3.HeaderOrReference
}

type serviceDef struct {
	Name        string
	Comment     string
	Methods     []*rpcDef
	methodNames map[string]int
}

type rpcDef struct {
	Name         string
	RequestType  string
	ResponseType string
	Comment      string
	HTTP         *httpRule
}

type httpRule struct {
	Method       string
	Path         string
	Body         string
	ResponseBody string
	// Headers are static request headers to send on every call to this
	// operation, sourced from the x-openapi2proto-headers vendor extension.
	// Used for requirements the OpenAPI parameter model can't express, such
	// as an undocumented required beta opt-in header.
	Headers map[string]string
}

type messageDef struct {
	Name    string
	Comment string
	Fields  []*fieldDef
	Enums   []*enumDef // nested enum types

	// scopeIdents holds the exact identifiers occupying this message's scope:
	// field names plus, for each nested enum, its type name and (per proto3 C++
	// scoping) its hoisted value names. A nested enum is only emitted if none of
	// its names collide here; otherwise the field stays a string.
	scopeIdents   map[string]bool
	enumTypeNames map[string]int
}

type fieldDef struct {
	Name     string
	OrigName string // original OpenAPI param name (used as HTTP query key); empty means use Name
	Type     string
	MapValue string
	Repeated bool
	Number   int
	Comment  string
}

type enumDef struct {
	Name    string
	Comment string
	Values  []*enumValueDef
}

type enumValueDef struct {
	Name    string
	Number  int
	Comment string
}

type protoType struct {
	Type     string
	MapValue string
	Repeated bool
}

type operationInfo struct {
	Method   string
	Path     string
	PathItem *openapiv3.PathItem
	Op       *openapiv3.Operation
}

type resolvedResponse struct {
	Code     string
	Response *openapiv3.Response
}

// GenerateGoService produces a Go source file implementing gRPC handlers that
// bridge to the REST API described in doc. goModule is the module path of the
// consuming project; pbSubPath is the relative import path where protoc places
// the generated pb files (e.g. "pb/cloudflare/zones"); runtimeImport is the
// Go import path for the runtime package (defaults to
// "github.com/accretional/openapi2proto/runtime" if empty).
func GenerateGoService(sourceName string, doc *openapiv3.Document, cfg Config, goModule, pbSubPath, runtimeImport string) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("nil OpenAPI document")
	}
	cfg = withDefaults(sourceName, cfg)
	g := &generator{
		cfg:              cfg,
		sourceName:       sourceName,
		doc:              doc,
		refs:             newRefResolver(doc),
		imports:          make(map[string]bool),
		services:         make(map[string]*serviceDef),
		serviceNames:     make(map[string]int),
		messageNames:     make(map[string]int),
		componentNames:   make(map[string]string),
		messages:         make(map[string]*messageDef),
		processingSchema: make(map[string]bool),
	}
	g.generateUniqueComponentNames()
	if err := g.generateComponents(); err != nil {
		return nil, err
	}
	if err := g.generateOperations(); err != nil {
		return nil, err
	}
	if runtimeImport == "" {
		runtimeImport = "github.com/accretional/openapi2proto/runtime"
	}
	return g.renderGoService(goModule, pbSubPath, runtimeImport), nil
}

func Generate(sourceName string, doc *openapiv3.Document, cfg Config) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("nil OpenAPI document")
	}
	cfg = withDefaults(sourceName, cfg)
	g := &generator{
		cfg:              cfg,
		sourceName:       sourceName,
		doc:              doc,
		refs:             newRefResolver(doc),
		imports:          make(map[string]bool),
		services:         make(map[string]*serviceDef),
		serviceNames:     make(map[string]int),
		messageNames:     make(map[string]int),
		componentNames:   make(map[string]string),
		messages:         make(map[string]*messageDef),
		processingSchema: make(map[string]bool),
	}
	g.generateUniqueComponentNames()
	if err := g.generateComponents(); err != nil {
		return nil, err
	}
	if err := g.generateOperations(); err != nil {
		return nil, err
	}
	return g.render(), nil
}

func withDefaults(sourceName string, cfg Config) Config {
	if cfg.ServiceGrouping == "" {
		cfg.ServiceGrouping = "tag"
	}
	if !cfg.HasHTTPPreference {
		cfg.EmitHTTPAnnotations = true
	}
	if cfg.PackageName == "" {
		base := strings.TrimSuffix(filepath.Base(sourceName), filepath.Ext(sourceName))
		cfg.PackageName = sanitizePackageName(base)
	} else {
		cfg.PackageName = sanitizeDottedPackageName(cfg.PackageName)
	}
	if cfg.GoPackage == "" {
		alias := strings.ReplaceAll(cfg.PackageName, ".", "")
		cfg.GoPackage = strings.ReplaceAll(cfg.PackageName, ".", "/") + ";" + alias
	}
	return cfg
}

func newRefResolver(doc *openapiv3.Document) *refResolver {
	r := &refResolver{
		schemas:       make(map[string]*openapiv3.SchemaOrReference),
		parameters:    make(map[string]*openapiv3.ParameterOrReference),
		responses:     make(map[string]*openapiv3.ResponseOrReference),
		requestBodies: make(map[string]*openapiv3.RequestBodyOrReference),
		headers:       make(map[string]*openapiv3.HeaderOrReference),
	}
	components := doc.GetComponents()
	if components == nil {
		return r
	}
	if schemas := components.GetSchemas(); schemas != nil {
		for _, item := range schemas.GetAdditionalProperties() {
			r.schemas[item.GetName()] = item.GetValue()
		}
	}
	if params := components.GetParameters(); params != nil {
		for _, item := range params.GetAdditionalProperties() {
			r.parameters[item.GetName()] = item.GetValue()
		}
	}
	if responses := components.GetResponses(); responses != nil {
		for _, item := range responses.GetAdditionalProperties() {
			r.responses[item.GetName()] = item.GetValue()
		}
	}
	if requestBodies := components.GetRequestBodies(); requestBodies != nil {
		for _, item := range requestBodies.GetAdditionalProperties() {
			r.requestBodies[item.GetName()] = item.GetValue()
		}
	}
	if headers := components.GetHeaders(); headers != nil {
		for _, item := range headers.GetAdditionalProperties() {
			r.headers[item.GetName()] = item.GetValue()
		}
	}
	return r
}

func (g *generator) generateUniqueComponentNames() {
	names := make([]string, 0, len(g.refs.schemas))
	for name := range g.refs.schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, raw := range names {
		g.componentNames[raw] = unique(toCamel(raw), g.messageNames)
	}
}

func (g *generator) generateComponents() error {
	names := make([]string, 0, len(g.refs.schemas))
	for name := range g.refs.schemas {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		schemaRef := g.refs.schemas[name]
		// Pure scalar schemas are inlined as primitive types wherever they are
		// referenced; no wrapper message is needed or emitted for them.
		if schema := schemaRef.GetSchema(); schema != nil && isPureScalarSchema(schema) {
			continue
		}
		// Freeform object schemas are inlined as google.protobuf.Struct wherever
		// referenced; no wrapper message is needed.
		if schema := schemaRef.GetSchema(); schema != nil && isFreeformObject(schema) {
			continue
		}
		// Likewise for schemas marked as an opaque, possibly-non-object value
		// (google.protobuf.Value): no wrapper message is needed.
		if schema := schemaRef.GetSchema(); schema != nil && isAnyValueSchema(schema) {
			continue
		}
		if _, err := g.ensureNamedSchemaMessage(name, schemaRef); err != nil {
			return err
		}
	}
	return nil
}

func (g *generator) generateOperations() error {
	paths := g.doc.GetPaths()
	if paths == nil {
		return nil
	}
	items := append([]*openapiv3.NamedPathItem(nil), paths.GetPath()...)
	sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
	for _, item := range items {
		for _, op := range operationsForPath(item.GetName(), item.GetValue()) {
			if err := g.generateOperation(op); err != nil {
				return err
			}
		}
	}
	return nil
}

func operationsForPath(path string, item *openapiv3.PathItem) []operationInfo {
	if item == nil {
		return nil
	}
	var ops []operationInfo
	appendOp := func(method string, op *openapiv3.Operation) {
		if op != nil {
			ops = append(ops, operationInfo{Method: method, Path: path, PathItem: item, Op: op})
		}
	}
	appendOp("GET", item.GetGet())
	appendOp("POST", item.GetPost())
	appendOp("PUT", item.GetPut())
	appendOp("PATCH", item.GetPatch())
	appendOp("DELETE", item.GetDelete())
	appendOp("OPTIONS", item.GetOptions())
	appendOp("HEAD", item.GetHead())
	appendOp("TRACE", item.GetTrace())
	return ops
}

func (g *generator) generateOperation(op operationInfo) error {
	service := g.serviceForOperation(op)
	rpcBase := g.operationName(op)
	rpcName := unique(rpcBase, service.methodNames)

	requestName := unique(rpcName+"Request", g.messageNames)
	responseName := unique(rpcName+"Response", g.messageNames)

	requestMsg, pathFields, bodyField, err := g.buildRequestMessage(requestName, op)
	if err != nil {
		return err
	}
	responseMsg, responseBodyField, err := g.buildResponseMessage(responseName, op)
	if err != nil {
		return err
	}
	g.addMessage(requestMsg)
	g.addMessage(responseMsg)

	rpc := &rpcDef{
		Name:         rpcName,
		RequestType:  requestMsg.Name,
		ResponseType: responseMsg.Name,
		Comment:      firstNonEmpty(op.Op.GetSummary(), op.Op.GetDescription()),
	}
	if g.cfg.EmitHTTPAnnotations {
		rpc.HTTP = &httpRule{
			Method:       op.Method,
			Path:         rewritePathTemplate(op.Path, pathFields),
			Body:         bodyField,
			ResponseBody: responseBodyField,
			Headers:      operationHeaders(op.Op),
		}
		g.imports["google/api/annotations.proto"] = true
	}
	service.Methods = append(service.Methods, rpc)
	return nil
}

func (g *generator) serviceForOperation(op operationInfo) *serviceDef {
	base := g.serviceName(op)
	name := base
	if _, ok := g.services[name]; !ok {
		name = unique(base, g.serviceNames)
		svc := &serviceDef{
			Name:        name,
			Comment:     fmt.Sprintf("Generated from %s operations.", firstNonEmpty(firstTag(op.Op), g.cfg.PackageName)),
			methodNames: make(map[string]int),
		}
		g.services[name] = svc
		g.serviceOrder = append(g.serviceOrder, name)
	}
	return g.services[name]
}

func (g *generator) serviceName(op operationInfo) string {
	if g.cfg.ServiceGrouping == "single" {
		return toCamel(lastPackageSegment(g.cfg.PackageName)) + "Service"
	}
	if tag := firstTag(op.Op); tag != "" {
		return toCamel(tag) + "Service"
	}
	return toCamel(lastPackageSegment(g.cfg.PackageName)) + "Service"
}

func (g *generator) operationName(op operationInfo) string {
	if id := op.Op.GetOperationId(); id != "" {
		return toCamel(id)
	}
	pathBits := strings.Trim(op.Path, "/")
	pathBits = strings.ReplaceAll(pathBits, "{", "By ")
	pathBits = strings.ReplaceAll(pathBits, "}", "")
	return toCamel(op.Method + " " + pathBits)
}

func firstTag(op *openapiv3.Operation) string {
	if op == nil || len(op.GetTags()) == 0 {
		return ""
	}
	return op.GetTags()[0]
}

func lastPackageSegment(pkg string) string {
	parts := strings.Split(pkg, ".")
	return parts[len(parts)-1]
}

func (g *generator) buildRequestMessage(name string, op operationInfo) (*messageDef, map[string]string, string, error) {
	msg := &messageDef{Name: name, Comment: firstNonEmpty(op.Op.GetSummary(), op.Op.GetDescription())}
	pathFields := make(map[string]string)
	used := make(map[string]int)
	fieldNo := 1

	for _, param := range g.collectParameters(op.PathItem, op.Op) {
		loc := param.GetIn()
		base := toSnake(param.GetName())
		switch loc {
		case "header", "cookie":
			base = loc + "_" + base
		case "query":
			if used[base] > 0 {
				base = "query_" + base
			}
		case "path":
			if used[base] > 0 {
				base = "path_" + base
			}
		default:
			base = loc + "_" + base
		}
		fieldName := uniqueField(base, used)
		pt, err := g.protoTypeForParameter(name+toCamel(param.GetName()), param)
		if err != nil {
			return nil, nil, "", err
		}
		origName := param.GetName()
		storedOrig := ""
		if origName != fieldName {
			storedOrig = origName
		}
		msg.Fields = append(msg.Fields, &fieldDef{
			Name:     fieldName,
			OrigName: storedOrig,
			Type:     pt.Type,
			MapValue: pt.MapValue,
			Repeated: pt.Repeated,
			Number:   fieldNo,
			Comment:  buildParameterComment(param),
		})
		fieldNo++
		if loc == "path" {
			pathFields[param.GetName()] = fieldName
		}
	}

	bodyFieldName := ""
	if reqBody, err := g.resolveRequestBody(op.Op.GetRequestBody()); err != nil {
		return nil, nil, "", err
	} else if reqBody != nil {
		mediaName, media := chooseMediaType(reqBody.GetContent())
		fieldName := uniqueField("body", used)
		var pt protoType
		if media != nil && media.GetSchema() != nil {
			pt, err = g.protoTypeForSchema(name+"Body", media.GetSchema())
			if err != nil {
				return nil, nil, "", err
			}
		} else {
			g.imports["google/api/httpbody.proto"] = true
			pt = protoType{Type: "google.api.HttpBody"}
		}
		msg.Fields = append(msg.Fields, &fieldDef{
			Name:     fieldName,
			Type:     pt.Type,
			MapValue: pt.MapValue,
			Repeated: pt.Repeated,
			Number:   fieldNo,
			Comment:  fmt.Sprintf("Request body from media type %q.", mediaName),
		})
		fieldNo++
		bodyFieldName = fieldName
	}

	return msg, pathFields, bodyFieldName, nil
}

func (g *generator) buildResponseMessage(name string, op operationInfo) (*messageDef, string, error) {
	msg := &messageDef{Name: name, Comment: "Canonical success response."}
	msg.Fields = append(msg.Fields, &fieldDef{
		Name:    "http_status_code",
		Type:    "int32",
		Number:  1,
		Comment: "HTTP status code represented by this response.",
	})

	fieldNo := 2
	bodyField := ""
	response, err := g.chooseResponse(op.Op)
	if err != nil {
		return nil, "", err
	}
	if response == nil {
		return msg, "", nil
	}

	mediaName, media := chooseMediaType(response.Response.GetContent())
	if media != nil {
		var pt protoType
		if media.GetSchema() != nil {
			pt, err = g.protoTypeForSchema(name+"Body", media.GetSchema())
			if err != nil {
				return nil, "", err
			}
		} else {
			g.imports["google/api/httpbody.proto"] = true
			pt = protoType{Type: "google.api.HttpBody"}
		}
		msg.Fields = append(msg.Fields, &fieldDef{
			Name:     "body",
			Type:     pt.Type,
			MapValue: pt.MapValue,
			Repeated: pt.Repeated,
			Number:   fieldNo,
			Comment:  fmt.Sprintf("Response body from media type %q.", mediaName),
		})
		fieldNo++
		bodyField = "body"
	}

	headers := response.Response.GetHeaders()
	if headers != nil {
		items := append([]*openapiv3.NamedHeaderOrReference(nil), headers.GetAdditionalProperties()...)
		sort.Slice(items, func(i, j int) bool { return items[i].GetName() < items[j].GetName() })
		used := map[string]int{"http_status_code": 1, "body": 1}
		for _, item := range items {
			header, err := g.resolveHeader(item.GetValue())
			if err != nil {
				return nil, "", err
			}
			if header == nil {
				continue
			}
			pt, err := g.protoTypeForHeader(name+toCamel(item.GetName()), header)
			if err != nil {
				return nil, "", err
			}
			msg.Fields = append(msg.Fields, &fieldDef{
				Name:     uniqueField("header_"+toSnake(item.GetName()), used),
				Type:     pt.Type,
				MapValue: pt.MapValue,
				Repeated: pt.Repeated,
				Number:   fieldNo,
				Comment:  buildHeaderComment(item.GetName(), header),
			})
			fieldNo++
		}
	}

	return msg, bodyField, nil
}

func (g *generator) collectParameters(pathItem *openapiv3.PathItem, op *openapiv3.Operation) []*openapiv3.Parameter {
	var ordered []*openapiv3.Parameter
	index := make(map[string]int)
	add := func(items []*openapiv3.ParameterOrReference) {
		for _, item := range items {
			param, err := g.resolveParameter(item)
			if err != nil || param == nil {
				continue
			}
			key := param.GetIn() + ":" + param.GetName()
			if i, ok := index[key]; ok {
				ordered[i] = param
				continue
			}
			index[key] = len(ordered)
			ordered = append(ordered, param)
		}
	}
	if pathItem != nil {
		add(pathItem.GetParameters())
	}
	if op != nil {
		add(op.GetParameters())
	}
	return ordered
}

func (g *generator) resolveParameter(paramRef *openapiv3.ParameterOrReference) (*openapiv3.Parameter, error) {
	if paramRef == nil {
		return nil, nil
	}
	if param := paramRef.GetParameter(); param != nil {
		return param, nil
	}
	ref := paramRef.GetReference()
	if ref == nil {
		return nil, nil
	}
	kind, name, err := parseRef(ref.GetXRef())
	if err != nil {
		return nil, err
	}
	if kind != "parameters" {
		return nil, fmt.Errorf("unsupported parameter ref %q", ref.GetXRef())
	}
	target := g.refs.parameters[name]
	if target == nil {
		return nil, fmt.Errorf("missing parameter ref %q", ref.GetXRef())
	}
	return target.GetParameter(), nil
}

func (g *generator) resolveRequestBody(bodyRef *openapiv3.RequestBodyOrReference) (*openapiv3.RequestBody, error) {
	if bodyRef == nil {
		return nil, nil
	}
	if body := bodyRef.GetRequestBody(); body != nil {
		return body, nil
	}
	ref := bodyRef.GetReference()
	if ref == nil {
		return nil, nil
	}
	kind, name, err := parseRef(ref.GetXRef())
	if err != nil {
		return nil, err
	}
	if kind != "requestBodies" {
		return nil, fmt.Errorf("unsupported request body ref %q", ref.GetXRef())
	}
	target := g.refs.requestBodies[name]
	if target == nil {
		return nil, fmt.Errorf("missing request body ref %q", ref.GetXRef())
	}
	return target.GetRequestBody(), nil
}

func (g *generator) chooseResponse(op *openapiv3.Operation) (*resolvedResponse, error) {
	if op == nil || op.GetResponses() == nil {
		return nil, nil
	}
	var candidates []*resolvedResponse
	for _, item := range op.GetResponses().GetResponseOrReference() {
		response, err := g.resolveResponse(item.GetValue())
		if err != nil {
			return nil, err
		}
		if response == nil {
			continue
		}
		candidates = append(candidates, &resolvedResponse{Code: item.GetName(), Response: response})
	}
	if def := op.GetResponses().GetDefault(); def != nil {
		response, err := g.resolveResponse(def)
		if err != nil {
			return nil, err
		}
		if response != nil {
			candidates = append(candidates, &resolvedResponse{Code: "default", Response: response})
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return responseRank(candidates[i].Code) < responseRank(candidates[j].Code)
	})
	return candidates[0], nil
}

func responseRank(code string) int {
	if code == "default" {
		return 1000
	}
	// Handle non-standard wildcard codes like "2XX", "2xx", "2xX" — treat as
	// a generic 2xx success response ranked just below explicit 200 codes.
	if len(code) == 3 && (code[0] == '2') &&
		(code[1] == 'x' || code[1] == 'X') &&
		(code[2] == 'x' || code[2] == 'X') {
		return 299
	}
	n, err := strconv.Atoi(code)
	if err != nil {
		return 2000
	}
	if n >= 200 && n < 300 {
		return n
	}
	return 1000 + n
}

func (g *generator) resolveResponse(respRef *openapiv3.ResponseOrReference) (*openapiv3.Response, error) {
	if respRef == nil {
		return nil, nil
	}
	if resp := respRef.GetResponse(); resp != nil {
		return resp, nil
	}
	ref := respRef.GetReference()
	if ref == nil {
		return nil, nil
	}
	kind, name, err := parseRef(ref.GetXRef())
	if err != nil {
		return nil, err
	}
	if kind != "responses" {
		return nil, fmt.Errorf("unsupported response ref %q", ref.GetXRef())
	}
	target := g.refs.responses[name]
	if target == nil {
		return nil, fmt.Errorf("missing response ref %q", ref.GetXRef())
	}
	return target.GetResponse(), nil
}

func (g *generator) resolveHeader(headerRef *openapiv3.HeaderOrReference) (*openapiv3.Header, error) {
	if headerRef == nil {
		return nil, nil
	}
	if header := headerRef.GetHeader(); header != nil {
		return header, nil
	}
	ref := headerRef.GetReference()
	if ref == nil {
		return nil, nil
	}
	kind, name, err := parseRef(ref.GetXRef())
	if err != nil {
		return nil, err
	}
	if kind != "headers" {
		return nil, fmt.Errorf("unsupported header ref %q", ref.GetXRef())
	}
	target := g.refs.headers[name]
	if target == nil {
		return nil, fmt.Errorf("missing header ref %q", ref.GetXRef())
	}
	return target.GetHeader(), nil
}

func chooseMediaType(mediaTypes *openapiv3.MediaTypes) (string, *openapiv3.MediaType) {
	if mediaTypes == nil {
		return "", nil
	}
	items := append([]*openapiv3.NamedMediaType(nil), mediaTypes.GetAdditionalProperties()...)
	if len(items) == 0 {
		return "", nil
	}
	sort.Slice(items, func(i, j int) bool {
		return mediaTypeRank(items[i].GetName()) < mediaTypeRank(items[j].GetName())
	})
	return items[0].GetName(), items[0].GetValue()
}

func mediaTypeRank(name string) int {
	switch name {
	case "application/json":
		return 0
	case "application/x-www-form-urlencoded":
		return 1
	case "multipart/form-data":
		return 2
	default:
		return 10
	}
}

func (g *generator) protoTypeForParameter(nameHint string, param *openapiv3.Parameter) (protoType, error) {
	if param == nil {
		g.imports["google/protobuf/struct.proto"] = true
		return protoType{Type: "google.protobuf.Struct"}, nil
	}
	if schema := param.GetSchema(); schema != nil {
		return g.protoTypeForSchema(nameHint, schema)
	}
	if _, media := chooseMediaType(param.GetContent()); media != nil && media.GetSchema() != nil {
		return g.protoTypeForSchema(nameHint, media.GetSchema())
	}
	return protoType{Type: "string"}, nil
}

func (g *generator) protoTypeForHeader(nameHint string, header *openapiv3.Header) (protoType, error) {
	if header == nil {
		return protoType{Type: "string"}, nil
	}
	if schema := header.GetSchema(); schema != nil {
		return g.protoTypeForSchema(nameHint, schema)
	}
	if _, media := chooseMediaType(header.GetContent()); media != nil && media.GetSchema() != nil {
		return g.protoTypeForSchema(nameHint, media.GetSchema())
	}
	return protoType{Type: "string"}, nil
}

func (g *generator) protoTypeForSchema(nameHint string, schemaRef *openapiv3.SchemaOrReference) (protoType, error) {
	if schemaRef == nil {
		g.imports["google/protobuf/struct.proto"] = true
		return protoType{Type: "google.protobuf.Struct"}, nil
	}
	if ref := schemaRef.GetReference(); ref != nil {
		kind, name, err := parseRef(ref.GetXRef())
		if err != nil {
			return protoType{}, err
		}
		if kind != "schemas" {
			return protoType{}, fmt.Errorf("unsupported schema ref %q", ref.GetXRef())
		}
		// If the referenced schema is a pure scalar, bare array, or freeform object,
		// inline it directly instead of generating a named wrapper message.
		// Guard against recursive schemas (e.g. a schema whose items reference itself).
		if !g.processingSchema[name] {
			if refSchemaRef := g.refs.schemas[name]; refSchemaRef != nil {
				if refSchema := refSchemaRef.GetSchema(); refSchema != nil {
					if isPureScalarSchema(refSchema) {
						return scalarProtoType(refSchema.GetType(), refSchema.GetFormat()), nil
					}
					if isFreeformObject(refSchema) {
						g.imports["google/protobuf/struct.proto"] = true
						return protoType{Type: "google.protobuf.Struct"}, nil
					}
					if isAnyValueSchema(refSchema) {
						g.imports["google/protobuf/struct.proto"] = true
						return protoType{Type: "google.protobuf.Value"}, nil
					}
					if (refSchema.GetType() == "array" || refSchema.GetItems() != nil) &&
						!isObjectSchema(refSchema) && !isMapSchema(refSchema) {
						g.processingSchema[name] = true
						item := firstItem(refSchema)
						itemType, err := g.protoTypeForSchema(nameHint+"Item", item)
						delete(g.processingSchema, name)
						if err != nil {
							return protoType{}, err
						}
						// Ensure the named message is still registered (for completeness)
						if _, err2 := g.ensureNamedSchemaMessage(name, refSchemaRef); err2 != nil {
							return protoType{}, err2
						}
						return protoType{Type: itemType.Type, Repeated: true}, nil
					}
				}
			}
		}
		typeName, err := g.ensureNamedSchemaMessage(name, g.refs.schemas[name])
		if err != nil {
			return protoType{}, err
		}
		return protoType{Type: typeName}, nil
	}
	return g.protoTypeForInlineSchema(nameHint, schemaRef.GetSchema())
}

func (g *generator) protoTypeForInlineSchema(nameHint string, schema *openapiv3.Schema) (protoType, error) {
	if schema == nil {
		g.imports["google/protobuf/struct.proto"] = true
		return protoType{Type: "google.protobuf.Struct"}, nil
	}
	if isMapSchema(schema) {
		valueType, err := g.mapValueType(nameHint+"Entry", schema.GetAdditionalProperties().GetSchemaOrReference())
		if err == nil && valueType != "" {
			return protoType{Type: "map", MapValue: valueType}, nil
		}
	}
	if isFreeformObject(schema) {
		g.imports["google/protobuf/struct.proto"] = true
		return protoType{Type: "google.protobuf.Struct"}, nil
	}
	if isAnyValueSchema(schema) {
		g.imports["google/protobuf/struct.proto"] = true
		return protoType{Type: "google.protobuf.Value"}, nil
	}
	if isObjectSchema(schema) {
		name := unique(toCamel(nameHint), g.messageNames)
		msg := &messageDef{Name: name, Comment: firstNonEmpty(schema.GetTitle(), schema.GetDescription())}
		g.addMessage(msg)
		if err := g.fillMessageFromSchema(msg, schema); err != nil {
			return protoType{}, err
		}
		return protoType{Type: name}, nil
	}
	if schema.GetType() == "array" || schema.GetItems() != nil {
		item := firstItem(schema)
		// Freeform object array items → repeated google.protobuf.Struct
		if itemSchema := item.GetSchema(); itemSchema != nil && isFreeformObject(itemSchema) {
			g.imports["google/protobuf/struct.proto"] = true
			return protoType{Type: "google.protobuf.Struct", Repeated: true}, nil
		}
		// Opaque, possibly-non-object array items → repeated google.protobuf.Value
		if itemSchema := item.GetSchema(); itemSchema != nil && isAnyValueSchema(itemSchema) {
			g.imports["google/protobuf/struct.proto"] = true
			return protoType{Type: "google.protobuf.Value", Repeated: true}, nil
		}
		itemType, err := g.protoTypeForSchema(nameHint+"Item", item)
		if err != nil {
			return protoType{}, err
		}
		if itemType.MapValue != "" {
			wrapperName := unique(toCamel(nameHint)+"Item", g.messageNames)
			msg := &messageDef{Name: wrapperName, Comment: "Wrapper for array entries that cannot be expressed directly as repeated map fields."}
			msg.Fields = append(msg.Fields, &fieldDef{Name: "entry", Type: "map", MapValue: itemType.MapValue, Number: 1})
			g.addMessage(msg)
			return protoType{Type: wrapperName, Repeated: true}, nil
		}
		return protoType{Type: itemType.Type, Repeated: true}, nil
	}
	return scalarProtoType(schema.GetType(), schema.GetFormat()), nil
}

func (g *generator) mapValueType(nameHint string, schemaRef *openapiv3.SchemaOrReference) (string, error) {
	pt, err := g.protoTypeForSchema(nameHint, schemaRef)
	if err != nil {
		return "", err
	}
	if pt.MapValue != "" || pt.Repeated {
		g.imports["google/protobuf/struct.proto"] = true
		return "google.protobuf.Struct", nil
	}
	return pt.Type, nil
}

func scalarProtoType(typ, format string) protoType {
	switch typ {
	case "boolean":
		return protoType{Type: "bool"}
	case "integer":
		switch format {
		case "int32":
			return protoType{Type: "int32"}
		default:
			return protoType{Type: "int64"}
		}
	case "number":
		if format == "float" {
			return protoType{Type: "float"}
		}
		return protoType{Type: "double"}
	case "string", "":
		return protoType{Type: "string"}
	default:
		return protoType{Type: "string"}
	}
}

func isObjectSchema(schema *openapiv3.Schema) bool {
	if schema == nil {
		return false
	}
	if schema.GetType() == "object" {
		return true
	}
	if schema.GetProperties() != nil && len(schema.GetProperties().GetAdditionalProperties()) > 0 {
		return true
	}
	return len(schema.GetAllOf()) > 0 ||
		len(schema.GetAnyOf()) > 0 ||
		len(schema.GetOneOf()) > 0
}

// isPureScalarSchema reports whether a schema resolves to a single primitive
// value (string, number, integer, boolean) with no object properties, no array
// items, and no composition keywords that introduce properties. Such schemas
// are inlined directly as proto primitive types rather than wrapped in a
// one-field message.
//
// anyOf/oneOf/allOf are permitted as long as every inline sub-schema (no $ref)
// is itself a pure scalar — this handles patterns like TTL which is defined as
// anyOf [{number, min:30, max:86400}, {number, enum:[1]}].
func isPureScalarSchema(schema *openapiv3.Schema) bool {
	if schema == nil {
		return false
	}
	if isAnyValueSchema(schema) {
		return false
	}
	if schema.GetType() == "object" {
		return false
	}
	if schema.GetProperties() != nil && len(schema.GetProperties().GetAdditionalProperties()) > 0 {
		return false
	}
	if isMapSchema(schema) {
		return false
	}
	if schema.GetType() == "array" || schema.GetItems() != nil {
		return false
	}
	// For composition keywords, every inline sub-schema must also be a pure
	// scalar. A $ref sub-schema cannot be inspected without the ref resolver
	// here, so treat it conservatively as non-scalar.
	combined := append(append(schema.GetAllOf(), schema.GetAnyOf()...), schema.GetOneOf()...)
	for _, item := range combined {
		if item.GetReference() != nil {
			return false
		}
		if s := item.GetSchema(); s != nil && !isPureScalarSchema(s) {
			return false
		}
	}
	return true
}

func isFreeformObject(schema *openapiv3.Schema) bool {
	if schema == nil {
		return false
	}
	return schema.GetType() == "object" &&
		(schema.GetProperties() == nil || len(schema.GetProperties().GetAdditionalProperties()) == 0) &&
		schema.GetAdditionalProperties() == nil &&
		len(schema.GetAllOf()) == 0 &&
		len(schema.GetAnyOf()) == 0 &&
		len(schema.GetOneOf()) == 0
}

// anyValueMarker is the vendor-extension key normalizeStructuralSchema (in
// parse.go) stamps onto a genuinely heterogeneous anyOf/oneOf schema it can't
// resolve to a concrete proto type (e.g. string-or-array). It distinguishes
// that case from a schema the source document actually declared as a
// freeform object, so the two can map to different proto types below:
// google.protobuf.Value (any JSON value) instead of google.protobuf.Struct
// (JSON object only, which can't represent a scalar or array at the top
// level and would silently corrupt those branches on marshal).
const anyValueMarker = "x-openapi2proto-any-value"

// isAnyValueSchema reports whether schema was marked by normalizeStructuralSchema
// as an opaque, possibly-non-object JSON value.
func isAnyValueSchema(schema *openapiv3.Schema) bool {
	if schema == nil {
		return false
	}
	for _, ext := range schema.GetSpecificationExtension() {
		if ext.GetName() == anyValueMarker {
			return true
		}
	}
	return false
}

// isEnumSchema reports whether a schema should be emitted as a proto enum: a
// string-typed schema with an enumerated value set and no object/array/map
// structure. Google Discovery enums are always strings; integer "enums" (rare)
// are left as scalar fields.
func isEnumSchema(schema *openapiv3.Schema) bool {
	if schema == nil || len(schema.GetEnum()) == 0 {
		return false
	}
	if t := schema.GetType(); t != "" && t != "string" {
		return false
	}
	if schema.GetProperties() != nil && len(schema.GetProperties().GetAdditionalProperties()) > 0 {
		return false
	}
	return !isMapSchema(schema) && schema.GetItems() == nil
}

// operationHeadersExtension is the OpenAPI specification-extension key under
// which a preprocessing step (e.g. proto-openai's generate.py) can attach
// static per-operation request headers that aren't captured by the OpenAPI
// parameter model — for example an undocumented required beta opt-in header.
// Value is a JSON object of header name to header value.
const operationHeadersExtension = "x-openapi2proto-headers"

// operationHeaders decodes the x-openapi2proto-headers extension on op, if
// present. The extension's raw value is stored as YAML text (gnostic's
// compiler.Marshal re-serialises the parsed node to YAML, not JSON, even
// when the source document was JSON), so this uses a YAML decoder rather
// than encoding/json.
func operationHeaders(op *openapiv3.Operation) map[string]string {
	for _, ext := range op.GetSpecificationExtension() {
		if ext.GetName() != operationHeadersExtension {
			continue
		}
		var out map[string]string
		if err := yaml.Unmarshal([]byte(ext.GetValue().GetYaml()), &out); err == nil {
			return out
		}
	}
	return nil
}

// enumValueDescriptions decodes the x-enumDescriptions extension (a JSON array
// aligned with the schema's enum order) carried over from Discovery, if present.
func enumValueDescriptions(schema *openapiv3.Schema) []string {
	for _, ext := range schema.GetSpecificationExtension() {
		if ext.GetName() != enumDescriptionsExtension {
			continue
		}
		var out []string
		if err := json.Unmarshal([]byte(ext.GetValue().GetYaml()), &out); err == nil {
			return out
		}
	}
	return nil
}

// isValidProtoIdent reports whether s is a valid proto identifier
// ([A-Za-z][A-Za-z0-9_]*), the requirement for a proto enum value name.
func isValidProtoIdent(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	return true
}

// confusableEnumKey normalizes an enum value the way protoc does when rejecting
// confusable value names within an enum: lowercase and drop underscores, then
// strip the (similarly normalized) enum type name prefix. Because normalization
// happens first, it catches values that differ only in case/underscore style —
// e.g. "DEVICE_LICENSE_TYPE_UNSPECIFIED" and "deviceLicenseTypeUnspecified",
// or "ENGINE_MYSQL" and "MYSQL" under enum Engine — which protoc rejects.
func confusableEnumKey(enumType, value string) string {
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "")) }
	return strings.TrimPrefix(norm(value), norm(toScreamingSnake(enumType)))
}

// isZeroValueName reports whether an exact Discovery enum value should occupy
// proto enum number 0 (Google's *_UNSPECIFIED / *_UNKNOWN convention).
func isZeroValueName(ident string) bool {
	return ident == "UNSPECIFIED" || ident == "UNKNOWN" ||
		strings.HasSuffix(ident, "_UNSPECIFIED") || strings.HasSuffix(ident, "_UNKNOWN")
}

// addNestedEnum tries to add a proto enum (type named after propName, nested in
// msg) for an inline string-enum schema, using the exact Discovery value
// strings as the proto value names so the REST binding stays faithful: the
// runtime marshals bodies with protojson, which emits an enum field by its
// value name, and Google's REST API expects exactly the Discovery string.
//
// It returns the nested enum type name, or ("", false) when the values cannot
// be represented faithfully — invalid identifiers, values confusable under
// protoc's rules (e.g. "CODE_39" vs "CODE39"), or names that would collide with
// another enum already nested in this message (proto3 hoists nested enum values
// into the message scope). In that case the caller keeps the field as a string,
// matching the pre-enum behavior.
func (g *generator) addNestedEnum(msg *messageDef, propName string, schema *openapiv3.Schema) (string, bool) {
	descs := enumValueDescriptions(schema)

	type candidate struct{ name, comment string }
	var zero *candidate
	var rest []candidate
	exact := make(map[string]bool)
	for i, any := range schema.GetEnum() {
		raw := any.GetYaml()
		if !isValidProtoIdent(raw) {
			return "", false
		}
		if exact[raw] {
			continue // identical duplicate value; harmless to drop
		}
		exact[raw] = true
		comment := ""
		if i < len(descs) {
			comment = descs[i]
		}
		if zero == nil && isZeroValueName(raw) {
			zero = &candidate{raw, comment}
			continue
		}
		rest = append(rest, candidate{raw, comment})
	}
	if zero == nil && len(rest) == 0 {
		return "", false
	}

	if msg.enumTypeNames == nil {
		msg.enumTypeNames = make(map[string]int)
	}
	typeName := unique(toCamel(propName), msg.enumTypeNames)

	var values []*enumValueDef
	if zero != nil {
		values = append(values, &enumValueDef{Name: zero.name, Number: 0, Comment: zero.comment})
	} else {
		// proto3 requires value 0. Synthesize one; protojson omits zero-valued
		// enums from output, so a synthetic default is never sent on the wire.
		synth := toScreamingSnake(typeName) + "_UNSPECIFIED"
		if !isValidProtoIdent(synth) || exact[synth] {
			return "", false
		}
		values = append(values, &enumValueDef{Name: synth, Number: 0, Comment: "Default value; not a Discovery enum value."})
	}
	for i, c := range rest {
		values = append(values, &enumValueDef{Name: c.name, Number: i + 1, Comment: c.comment})
	}

	// protoc rejects two of an enum's values that are confusable after stripping
	// the enum-name prefix (e.g. "ENGINE_MYSQL" vs "MYSQL").
	confusable := make(map[string]bool)
	for _, v := range values {
		k := confusableEnumKey(typeName, v.Name)
		if confusable[k] {
			return "", false
		}
		confusable[k] = true
	}

	// proto3 C++ scoping: the enum type name and its hoisted value names must be
	// unique against everything already in the message scope (field names and
	// other nested enums). Any collision -> keep the field as a string.
	if msg.scopeIdents == nil {
		msg.scopeIdents = make(map[string]bool)
	}
	if msg.scopeIdents[typeName] {
		return "", false
	}
	for _, v := range values {
		if msg.scopeIdents[v.Name] {
			return "", false
		}
	}
	msg.scopeIdents[typeName] = true
	for _, v := range values {
		msg.scopeIdents[v.Name] = true
	}

	msg.Enums = append(msg.Enums, &enumDef{
		Name:    typeName,
		Comment: firstNonEmpty(schema.GetTitle(), schema.GetDescription()),
		Values:  values,
	})
	return typeName, true
}

func isMapSchema(schema *openapiv3.Schema) bool {
	if schema == nil || schema.GetAdditionalProperties() == nil {
		return false
	}
	if schema.GetProperties() != nil && len(schema.GetProperties().GetAdditionalProperties()) > 0 {
		return false
	}
	return schema.GetAdditionalProperties().GetSchemaOrReference() != nil
}

func firstItem(schema *openapiv3.Schema) *openapiv3.SchemaOrReference {
	if schema == nil || schema.GetItems() == nil {
		return nil
	}
	items := schema.GetItems().GetSchemaOrReference()
	if len(items) == 0 {
		return nil
	}
	return items[0]
}

func (g *generator) ensureNamedSchemaMessage(rawName string, schemaRef *openapiv3.SchemaOrReference) (string, error) {
	name := g.componentNames[rawName]
	if name == "" {
		name = unique(toCamel(rawName), g.messageNames)
		g.componentNames[rawName] = name
	}
	if _, ok := g.messages[name]; ok {
		return name, nil
	}
	msg := &messageDef{Name: name}
	g.addMessage(msg)
	schema := schemaRef.GetSchema()
	if schema != nil {
		msg.Comment = firstNonEmpty(schema.GetTitle(), schema.GetDescription())
	}
	if err := g.fillMessageFromSchema(msg, schema); err != nil {
		return "", err
	}
	return name, nil
}

func (g *generator) fillMessageFromSchema(msg *messageDef, schema *openapiv3.Schema) error {
	if schema == nil {
		g.imports["google/protobuf/struct.proto"] = true
		msg.Fields = append(msg.Fields, &fieldDef{Name: "data", Type: "google.protobuf.Struct", Number: 1, Comment: "Unstructured schema."})
		return nil
	}
	props, additional, err := g.collectProperties(schema)
	if err != nil {
		return err
	}
	if len(props) > 0 {
		used := make(map[string]int)
		// Seed the message scope with every field name so a nested enum whose
		// type/value names would collide (proto3 C++ scoping) falls back to string.
		if msg.scopeIdents == nil {
			msg.scopeIdents = make(map[string]bool)
		}
		for _, prop := range props {
			msg.scopeIdents[toSnake(prop.GetName())] = true
		}
		msg.scopeIdents["additional_properties"] = true
		fieldNo := 1
		for _, prop := range props {
			fieldName := uniqueField(toSnake(prop.GetName()), used)
			// An inline string enum becomes a typed nested enum when it can be
			// represented faithfully; otherwise it stays a string (see addNestedEnum).
			if ps := prop.GetValue().GetSchema(); ps != nil && isEnumSchema(ps) {
				if enumType, ok := g.addNestedEnum(msg, prop.GetName(), ps); ok {
					msg.Fields = append(msg.Fields, &fieldDef{
						Name:    fieldName,
						Type:    enumType,
						Number:  fieldNo,
						Comment: propComment(prop.GetName(), prop.GetValue()),
					})
					fieldNo++
					continue
				}
			}
			pt, err := g.protoTypeForSchema(msg.Name+toCamel(prop.GetName()), prop.GetValue())
			if err != nil {
				return err
			}
			msg.Fields = append(msg.Fields, &fieldDef{
				Name:     fieldName,
				Type:     pt.Type,
				MapValue: pt.MapValue,
				Repeated: pt.Repeated,
				Number:   fieldNo,
				Comment:  propComment(prop.GetName(), prop.GetValue()),
			})
			fieldNo++
		}
		if additional != nil && additional.GetSchemaOrReference() != nil {
			valueType, err := g.mapValueType(msg.Name+"AdditionalProperty", additional.GetSchemaOrReference())
			if err != nil {
				return err
			}
			msg.Fields = append(msg.Fields, &fieldDef{
				Name:     uniqueField("additional_properties", used),
				Type:     "map",
				MapValue: valueType,
				Number:   fieldNo,
				Comment:  "Additional undeclared properties.",
			})
		}
		return nil
	}
	if isMapSchema(schema) {
		valueType, err := g.mapValueType(msg.Name+"Entry", schema.GetAdditionalProperties().GetSchemaOrReference())
		if err != nil {
			return err
		}
		msg.Fields = append(msg.Fields, &fieldDef{
			Name:     "entries",
			Type:     "map",
			MapValue: valueType,
			Number:   1,
			Comment:  "Map-style schema values.",
		})
		return nil
	}
	if schema.GetType() == "array" || schema.GetItems() != nil {
		pt, err := g.protoTypeForSchema(msg.Name+"Item", firstItem(schema))
		if err != nil {
			return err
		}
		if pt.MapValue != "" {
			wrapperName := unique(msg.Name+"Item", g.messageNames)
			wrapper := &messageDef{Name: wrapperName, Comment: "Wrapper for map array entries."}
			wrapper.Fields = append(wrapper.Fields, &fieldDef{Name: "entry", Type: "map", MapValue: pt.MapValue, Number: 1})
			g.addMessage(wrapper)
			pt = protoType{Type: wrapperName}
		}
		msg.Fields = append(msg.Fields, &fieldDef{
			Name:     "items",
			Type:     pt.Type,
			Repeated: true,
			Number:   1,
			Comment:  "Array values.",
		})
		return nil
	}
	if isFreeformObject(schema) {
		g.imports["google/protobuf/struct.proto"] = true
		msg.Fields = append(msg.Fields, &fieldDef{Name: "data", Type: "google.protobuf.Struct", Number: 1, Comment: "Unstructured object."})
		return nil
	}
	if isAnyValueSchema(schema) {
		g.imports["google/protobuf/struct.proto"] = true
		msg.Fields = append(msg.Fields, &fieldDef{Name: "value", Type: "google.protobuf.Value", Number: 1, Comment: "Opaque JSON value."})
		return nil
	}
	pt := scalarProtoType(schema.GetType(), schema.GetFormat())
	msg.Fields = append(msg.Fields, &fieldDef{
		Name:    "value",
		Type:    pt.Type,
		Number:  1,
		Comment: "Scalar value wrapper.",
	})
	return nil
}

func (g *generator) collectProperties(schema *openapiv3.Schema) ([]*openapiv3.NamedSchemaOrReference, *openapiv3.AdditionalPropertiesItem, error) {
	props := make(map[string]*openapiv3.NamedSchemaOrReference)
	order := make([]string, 0)
	var additional *openapiv3.AdditionalPropertiesItem

	var visit func(*openapiv3.Schema) error
	visit = func(current *openapiv3.Schema) error {
		if current == nil {
			return nil
		}
		if current.GetProperties() != nil {
			for _, prop := range current.GetProperties().GetAdditionalProperties() {
				if _, ok := props[prop.GetName()]; !ok {
					order = append(order, prop.GetName())
				}
				props[prop.GetName()] = prop
			}
		}
		if additional == nil && current.GetAdditionalProperties() != nil {
			additional = current.GetAdditionalProperties()
		}
		visitItems := func(items []*openapiv3.SchemaOrReference) error {
			for _, item := range items {
				if item.GetReference() != nil {
					kind, name, err := parseRef(item.GetReference().GetXRef())
					if err != nil {
						return err
					}
					if kind != "schemas" {
						continue
					}
					target := g.refs.schemas[name]
					if target == nil {
						return fmt.Errorf("missing schema ref %q", item.GetReference().GetXRef())
					}
					if err := visit(target.GetSchema()); err != nil {
						return err
					}
					continue
				}
				if err := visit(item.GetSchema()); err != nil {
					return err
				}
			}
			return nil
		}
		if err := visitItems(current.GetAllOf()); err != nil {
			return err
		}
		if err := visitItems(current.GetAnyOf()); err != nil {
			return err
		}
		if err := visitItems(current.GetOneOf()); err != nil {
			return err
		}
		return nil
	}

	if err := visit(schema); err != nil {
		return nil, nil, err
	}

	out := make([]*openapiv3.NamedSchemaOrReference, 0, len(order))
	for _, name := range order {
		out = append(out, props[name])
	}
	return out, additional, nil
}

func parseRef(ref string) (string, string, error) {
	parts := strings.Split(ref, "/")
	if len(parts) != 4 || parts[0] != "#" || parts[1] != "components" {
		return "", "", fmt.Errorf("unsupported ref %q", ref)
	}
	return parts[2], parts[3], nil
}

func rewritePathTemplate(path string, pathFields map[string]string) string {
	if len(pathFields) == 0 {
		return path
	}
	replacer := func(segment string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(segment, "{"), "}")
		if field, ok := pathFields[name]; ok {
			return "{" + field + "}"
		}
		return segment
	}
	var out strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] != '{' {
			out.WriteByte(path[i])
			continue
		}
		j := i + 1
		for j < len(path) && path[j] != '}' {
			j++
		}
		if j >= len(path) {
			out.WriteString(path[i:])
			break
		}
		out.WriteString(replacer(path[i : j+1]))
		i = j
	}
	return out.String()
}

func buildParameterComment(param *openapiv3.Parameter) string {
	if param == nil {
		return ""
	}
	desc := firstNonEmpty(param.GetDescription(), fmt.Sprintf("OpenAPI %s parameter %q.", param.GetIn(), param.GetName()))
	if param.GetRequired() {
		desc += " Required."
	}
	return desc
}

func buildHeaderComment(name string, header *openapiv3.Header) string {
	if header == nil {
		return fmt.Sprintf("Response header %q.", name)
	}
	return firstNonEmpty(header.GetDescription(), fmt.Sprintf("Response header %q.", name))
}

func propComment(name string, schemaRef *openapiv3.SchemaOrReference) string {
	if schemaRef == nil {
		return ""
	}
	if schema := schemaRef.GetSchema(); schema != nil {
		return firstNonEmpty(schema.GetDescription(), schema.GetTitle())
	}
	return fmt.Sprintf("OpenAPI property %q.", name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (g *generator) addMessage(msg *messageDef) {
	if _, exists := g.messages[msg.Name]; exists {
		return
	}
	g.messages[msg.Name] = msg
	g.messageOrder = append(g.messageOrder, msg.Name)
}
