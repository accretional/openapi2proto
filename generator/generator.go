package generator

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	openapiv3 "github.com/google/gnostic/openapiv3"
)

type Config struct {
	PackageName         string
	GoPackage           string
	ServiceGrouping     string
	EmitHTTPAnnotations bool
	HasHTTPPreference   bool
}

type generator struct {
	cfg            Config
	sourceName     string
	doc            *openapiv3.Document
	refs           *refResolver
	imports        map[string]bool
	services       map[string]*serviceDef
	serviceOrder   []string
	serviceNames   map[string]int
	messageNames   map[string]int
	componentNames map[string]string
	messages       map[string]*messageDef
	messageOrder   []string
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
}

type messageDef struct {
	Name    string
	Comment string
	Fields  []*fieldDef
}

type fieldDef struct {
	Name     string
	Type     string
	MapValue string
	Repeated bool
	Number   int
	Comment  string
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

func Generate(sourceName string, doc *openapiv3.Document, cfg Config) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("nil OpenAPI document")
	}
	cfg = withDefaults(sourceName, cfg)
	g := &generator{
		cfg:            cfg,
		sourceName:     sourceName,
		doc:            doc,
		refs:           newRefResolver(doc),
		imports:        make(map[string]bool),
		services:       make(map[string]*serviceDef),
		serviceNames:   make(map[string]int),
		messageNames:   make(map[string]int),
		componentNames: make(map[string]string),
		messages:       make(map[string]*messageDef),
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
		if _, err := g.ensureNamedSchemaMessage(name, g.refs.schemas[name]); err != nil {
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
		msg.Fields = append(msg.Fields, &fieldDef{
			Name:     fieldName,
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
		// If the referenced schema is a bare array wrapper, inline it as repeated
		// instead of generating a named wrapper message.
		if refSchemaRef := g.refs.schemas[name]; refSchemaRef != nil {
			if refSchema := refSchemaRef.GetSchema(); refSchema != nil {
				if (refSchema.GetType() == "array" || refSchema.GetItems() != nil) &&
					!isObjectSchema(refSchema) && !isMapSchema(refSchema) {
					item := firstItem(refSchema)
					itemType, err := g.protoTypeForSchema(nameHint+"Item", item)
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
		fieldNo := 1
		for _, prop := range props {
			fieldName := uniqueField(toSnake(prop.GetName()), used)
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
