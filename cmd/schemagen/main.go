// Copyright 2017 Google LLC. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Adapted from github.com/google/gnostic/openapiv3/schema-generator
//
// schemagen parses an official OAI OpenAPI specification markdown file and
// produces a JSON Schema describing the format. This JSON Schema is the
// input to the next pipeline stage (generate-gnostic).
//
// Usage:
//
//	go run ./cmd/schemagen <spec-file.md>
package main

import (
	"fmt"
	"log"
	"os"

	"github.com/accretional/openapi2proto/internal/jsonschema"
	"github.com/accretional/openapi2proto/internal/schemagen"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("Usage: schemagen <spec-file.md>")
	}
	specFile := os.Args[1]

	cfg, err := schemagen.DetectSpecConfig(specFile)
	if err != nil {
		log.Fatalf("Error detecting spec config: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Detected OpenAPI %s format\n", cfg.Version)

	model, err := schemagen.NewSchemaModel(specFile, cfg)
	if err != nil {
		log.Fatalf("Error parsing spec: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Found %d schema objects\n", len(model.Objects))
	for _, obj := range model.Objects {
		fmt.Fprintf(os.Stderr, "  %-35s (id=%-25s fixed=%d patterned=%d required=%v extendable=%v)\n",
			obj.Name, obj.ID, len(obj.FixedFields), len(obj.PatternedFields), obj.RequiredFields, obj.Extendable)
	}

	// Build the top-level schema from the "oas" model object
	builder := schemagen.NewSchemaBuilder()

	oasModel := model.ObjectWithID("oas")
	if oasModel == nil {
		log.Fatal("Unable to find OAS model. Has the source document structure changed?")
	}
	schema := builder.BuildSchemaWithModel(oasModel)

	// Set top-level schema metadata
	switch cfg.Version {
	case "3.2":
		schema.Title = stringptr("A JSON Schema for OpenAPI 3.2.")
		schema.ID = stringptr("https://spec.openapis.org/oas/3.2/schema")
	default:
		schema.Title = stringptr("A JSON Schema for OpenAPI 3.1.")
		schema.ID = stringptr("https://spec.openapis.org/oas/3.1/schema")
	}
	schema.Schema = stringptr("http://json-schema.org/draft-04/schema#")

	// Build definitions for all non-root objects
	definitions := make([]*jsonschema.NamedSchema, 0)
	schema.Definitions = &definitions

	for _, modelObject := range model.Objects {
		if modelObject.ID == "oas" {
			continue
		}
		definitionSchema := builder.BuildSchemaWithModel(&modelObject)
		name := modelObject.ID
		if name == "externalDocumentation" {
			name = "externalDocs"
		}
		*schema.Definitions = append(*schema.Definitions, jsonschema.NewNamedSchema(name, definitionSchema))
	}

	// Copy properties from parameterObject to headerObject if header has no properties of its own.
	// In the old gnostic format, the Header Object had no Fixed Fields table.
	// In the new OAI format (3.1.1+), it does — so this is conditional.
	headerObject := schema.DefinitionWithName("header")
	parameterObject := schema.DefinitionWithName("parameter")
	if parameterObject != nil && headerObject != nil &&
		(headerObject.Properties == nil || len(*headerObject.Properties) == 0) {
		newArray := make([]*jsonschema.NamedSchema, 0)
		for _, property := range *(parameterObject.Properties) {
			if property.Name != "name" && property.Name != "in" {
				newArray = append(newArray, property)
			}
		}
		headerObject.Properties = &newArray
		ppArray := make([]*jsonschema.NamedSchema, 0)
		ppArray = append(ppArray, *(parameterObject.PatternProperties)...)
		headerObject.PatternProperties = &ppArray
	}

	// Generate implied union types and map types
	builder.GenerateUnionTypes(schema)
	builder.GenerateMapTypes(schema)

	// Add hardcoded schema definitions: object, any, expression
	addBuiltinDefinitions(schema)

	// Force JSON Schema keywords into the "schema" definition
	addSchemaKeywords(schema)

	// Fix the content object
	fixContentObject(schema)

	// Fix the contact object
	fixContactObject(schema)

	// In OpenAPI 3.1, $ref is now a keyword within Schema itself, so the spec
	// defines many schema-typed fields as just "Schema" instead of "Schema | Reference".
	// However, the generator pipeline and proto-openai expect SchemaOrReference wrappers.
	// Upgrade these fields to schemaOrReference and schemas→schemasOrReferences for compat.
	upgradeSchemaOrRefCompat(schema)

	// Write the schema as JSON
	output := schema.JSONString()
	err = os.WriteFile("schema.json", []byte(output), 0644)
	if err != nil {
		log.Fatalf("Error writing schema.json: %v", err)
	}

	fmt.Fprintf(os.Stderr, "Wrote schema.json (%d bytes)\n", len(output))
}

func addBuiltinDefinitions(schema *jsonschema.Schema) {
	// "object" type
	{
		s := &jsonschema.Schema{}
		s.Type = jsonschema.NewStringOrStringArrayWithString("object")
		s.AdditionalProperties = jsonschema.NewSchemaOrBooleanWithBoolean(true)
		*schema.Definitions = append(*schema.Definitions, jsonschema.NewNamedSchema("object", s))
	}
	// "any" type
	{
		s := &jsonschema.Schema{}
		s.AdditionalProperties = jsonschema.NewSchemaOrBooleanWithBoolean(true)
		*schema.Definitions = append(*schema.Definitions, jsonschema.NewNamedSchema("any", s))
	}
	// "expression" type
	{
		s := &jsonschema.Schema{}
		s.Type = jsonschema.NewStringOrStringArrayWithString("object")
		s.AdditionalProperties = jsonschema.NewSchemaOrBooleanWithBoolean(true)
		*schema.Definitions = append(*schema.Definitions, jsonschema.NewNamedSchema("expression", s))
	}
	// "specificationExtension" type
	{
		s := &jsonschema.Schema{}
		s.Description = stringptr("Any property starting with x- is valid.")
		oneOf := make([]*jsonschema.Schema, 0)
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("null")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("number")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("boolean")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("object")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("array")})
		s.OneOf = &oneOf
		*schema.Definitions = append(*schema.Definitions, jsonschema.NewNamedSchema("specificationExtension", s))
	}
	// "defaultType" type
	{
		s := &jsonschema.Schema{}
		oneOf := make([]*jsonschema.Schema, 0)
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("null")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("array")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("object")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("number")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("boolean")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
		s.OneOf = &oneOf
		*schema.Definitions = append(*schema.Definitions, jsonschema.NewNamedSchema("defaultType", s))
	}
}

func addSchemaKeywords(schema *jsonschema.Schema) {
	schemaObject := schema.DefinitionWithName("schema")
	if schemaObject == nil {
		return
	}

	schemaObject.CopyOfficialSchemaProperties(
		[]string{
			"title",
			"multipleOf",
			"maximum",
			"exclusiveMaximum",
			"minimum",
			"exclusiveMinimum",
			"maxLength",
			"minLength",
			"pattern",
			"maxItems",
			"minItems",
			"uniqueItems",
			"maxProperties",
			"minProperties",
			"required",
			"enum",
		})
	schemaObject.AdditionalProperties = jsonschema.NewSchemaOrBooleanWithBoolean(false)
	schemaObject.AddProperty("type", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
	schemaObject.AddProperty("allOf", schemagen.ArrayOfSchema())
	schemaObject.AddProperty("oneOf", schemagen.ArrayOfSchema())
	schemaObject.AddProperty("anyOf", schemagen.ArrayOfSchema())
	schemaObject.AddProperty("not", &jsonschema.Schema{Ref: stringptr("#/definitions/schema")})

	anyOf := make([]*jsonschema.Schema, 0)
	anyOf = append(anyOf, &jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")})
	anyOf = append(anyOf, schemagen.ArrayOfSchema())
	schemaObject.AddProperty("items", &jsonschema.Schema{AnyOf: &anyOf})

	schemaObject.AddProperty("properties", &jsonschema.Schema{
		Type: jsonschema.NewStringOrStringArrayWithString("object"),
		AdditionalProperties: jsonschema.NewSchemaOrBooleanWithSchema(
			&jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")}),
	})

	{
		oneOf := make([]*jsonschema.Schema, 0)
		oneOf = append(oneOf, &jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("boolean")})
		schemaObject.AddProperty("additionalProperties", &jsonschema.Schema{OneOf: &oneOf})
	}

	schemaObject.AddProperty("default", &jsonschema.Schema{Ref: stringptr("#/definitions/defaultType")})
	schemaObject.AddProperty("description", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
	schemaObject.AddProperty("format", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})

	// JSON Schema 2020-12 / OpenAPI 3.1 keywords
	schemaObject.AddProperty("const", &jsonschema.Schema{Ref: stringptr("#/definitions/defaultType")})
	schemaObject.AddProperty("$id", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
	schemaObject.AddProperty("$anchor", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
	schemaObject.AddProperty("$comment", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
	schemaObject.AddProperty("$schema", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
	schemaObject.AddProperty("$dynamicRef", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
	schemaObject.AddProperty("$dynamicAnchor", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
	schemaObject.AddProperty("$recursiveRef", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
	schemaObject.AddProperty("$recursiveAnchor", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("boolean")})
	schemaObject.AddProperty("$vocabulary", &jsonschema.Schema{
		Type: jsonschema.NewStringOrStringArrayWithString("object"),
		AdditionalProperties: jsonschema.NewSchemaOrBooleanWithSchema(
			&jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("boolean")}),
	})
	schemaObject.AddProperty("$defs", &jsonschema.Schema{
		Type: jsonschema.NewStringOrStringArrayWithString("object"),
		AdditionalProperties: jsonschema.NewSchemaOrBooleanWithSchema(
			&jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")}),
	})
	schemaObject.AddProperty("propertyNames", &jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")})
	schemaObject.AddProperty("prefixItems", schemagen.ArrayOfSchema())
	schemaObject.AddProperty("contains", &jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")})
	schemaObject.AddProperty("if", &jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")})
	schemaObject.AddProperty("then", &jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")})
	schemaObject.AddProperty("else", &jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")})
	schemaObject.AddProperty("dependentSchemas", &jsonschema.Schema{
		Type: jsonschema.NewStringOrStringArrayWithString("object"),
		AdditionalProperties: jsonschema.NewSchemaOrBooleanWithSchema(
			&jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")}),
	})
	schemaObject.AddProperty("dependentRequired", &jsonschema.Schema{
		Type: jsonschema.NewStringOrStringArrayWithString("object"),
		AdditionalProperties: jsonschema.NewSchemaOrBooleanWithSchema(
			&jsonschema.Schema{
				Type: jsonschema.NewStringOrStringArrayWithString("array"),
				Items: &jsonschema.SchemaOrSchemaArray{
					Schema: &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")},
				},
			}),
	})
	schemaObject.AddProperty("contentEncoding", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
	schemaObject.AddProperty("contentMediaType", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("string")})
	{
		oneOf := make([]*jsonschema.Schema, 0)
		oneOf = append(oneOf, &jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("boolean")})
		schemaObject.AddProperty("unevaluatedItems", &jsonschema.Schema{OneOf: &oneOf})
	}
	{
		oneOf := make([]*jsonschema.Schema, 0)
		oneOf = append(oneOf, &jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")})
		oneOf = append(oneOf, &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("boolean")})
		schemaObject.AddProperty("unevaluatedProperties", &jsonschema.Schema{OneOf: &oneOf})
	}
	schemaObject.AddProperty("examples", &jsonschema.Schema{
		Type: jsonschema.NewStringOrStringArrayWithString("array"),
		Items: &jsonschema.SchemaOrSchemaArray{
			Schema: &jsonschema.Schema{Ref: stringptr("#/definitions/defaultType")},
		},
	})

	// OpenAPI Schema Object keywords (beyond JSON Schema)
	schemaObject.AddProperty("nullable", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("boolean")})
	schemaObject.AddProperty("deprecated", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("boolean")})
	schemaObject.AddProperty("readOnly", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("boolean")})
	schemaObject.AddProperty("writeOnly", &jsonschema.Schema{Type: jsonschema.NewStringOrStringArrayWithString("boolean")})
}

func fixContentObject(schema *jsonschema.Schema) {
	contentObject := schema.DefinitionWithName("content")
	if contentObject != nil {
		pairs := make([]*jsonschema.NamedSchema, 0)
		contentObject.PatternProperties = &pairs
		namedSchema := &jsonschema.NamedSchema{Name: "^", Value: &jsonschema.Schema{Ref: stringptr("#/definitions/mediaType")}}
		*(contentObject.PatternProperties) = append(*(contentObject.PatternProperties), namedSchema)
	}
}

func fixContactObject(schema *jsonschema.Schema) {
	contactObject := schema.DefinitionWithName("contact")
	if contactObject != nil {
		emailProperty := contactObject.PropertyWithName("email")
		if emailProperty != nil {
			emailProperty.Format = stringptr("email")
		}
		urlProperty := contactObject.PropertyWithName("url")
		if urlProperty != nil {
			urlProperty.Format = stringptr("uri")
		}
	}
}

// upgradeSchemaOrRefCompat rewrites fields that 3.1 changed from "Schema | Reference"
// to just "Schema" so they still use the schemaOrReference union type. This keeps the
// generated proto/Go compatible with the generator pipeline that expects SchemaOrReference.
func upgradeSchemaOrRefCompat(schema *jsonschema.Schema) {
	schemaOrRef := stringptr("#/definitions/schemaOrReference")
	schemasOrRefs := stringptr("#/definitions/schemasOrReferences")

	// components.schemas: schemas → schemasOrReferences
	compDef := schema.DefinitionWithName("components")
	if compDef != nil {
		prop := compDef.PropertyWithName("schemas")
		if prop != nil && prop.Ref != nil && *prop.Ref == "#/definitions/schemas" {
			prop.Ref = schemasOrRefs
		}
	}
	// Ensure schemasOrReferences definition exists (map of schemaOrReference)
	if schema.DefinitionWithName("schemasOrReferences") == nil {
		s := &jsonschema.Schema{
			Type: jsonschema.NewStringOrStringArrayWithString("object"),
			AdditionalProperties: jsonschema.NewSchemaOrBooleanWithSchema(
				&jsonschema.Schema{Ref: schemaOrRef}),
		}
		*schema.Definitions = append(*schema.Definitions, jsonschema.NewNamedSchema("schemasOrReferences", s))
	}

	// mediaType.schema, parameter.schema, header.schema: schema → schemaOrReference
	for _, defName := range []string{"mediaType", "parameter", "header"} {
		def := schema.DefinitionWithName(defName)
		if def == nil {
			continue
		}
		prop := def.PropertyWithName("schema")
		if prop != nil && prop.Ref != nil && *prop.Ref == "#/definitions/schema" {
			prop.Ref = schemaOrRef
		}
	}

	// discriminator.mapping uses strings already, no change needed.
}

func stringptr(input string) *string {
	return &input
}
