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

package schemagen

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/accretional/openapi2proto/internal/jsonschema"
)

// lowerFirst converts the first character of a string to lower case.
func lowerFirst(s string) string {
	if s == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToLower(r)) + s[n:]
}

// UnionType represents a union of two types.
type UnionType struct {
	Name        string
	ObjectType1 string
	ObjectType2 string
}

// MapType represents a map of a specified type (with string keys).
type MapType struct {
	Name       string
	ObjectType string
}

// SchemaBuilder holds state accumulated during schema construction.
type SchemaBuilder struct {
	UnionTypes map[string]*UnionType
	MapTypes   map[string]*MapType
}

// NewSchemaBuilder creates a new SchemaBuilder.
func NewSchemaBuilder() *SchemaBuilder {
	return &SchemaBuilder{
		UnionTypes: make(map[string]*UnionType),
		MapTypes:   make(map[string]*MapType),
	}
}

func (b *SchemaBuilder) noteUnionType(typeName, objectType1, objectType2 string) {
	b.UnionTypes[typeName] = &UnionType{
		Name:        typeName,
		ObjectType1: objectType1,
		ObjectType2: objectType2,
	}
}

func (b *SchemaBuilder) noteMapType(typeName, objectType string) {
	b.MapTypes[typeName] = &MapType{
		Name:       typeName,
		ObjectType: objectType,
	}
}

func (b *SchemaBuilder) definitionNameForType(typeName string) string {
	name := typeName
	switch typeName {
	case "OAuthFlows":
		name = "oauthFlows"
	case "OAuthFlow":
		name = "oauthFlow"
	case "XML":
		name = "xml"
	case "ExternalDocumentation":
		name = "externalDocs"
	default:
		if parts := strings.Split(typeName, "OR"); len(parts) > 1 {
			name = lowerFirst(parts[0]) + "Or" + parts[1]
			b.noteUnionType(name, parts[0], parts[1])
		} else {
			name = lowerFirst(typeName)
		}
	}
	return "#/definitions/" + name
}

func pluralize(name string) string {
	if name == "any" {
		return "anys"
	}
	switch name[len(name)-1] {
	case 'y':
		name = name[0:len(name)-1] + "ies"
	case 's':
		name = name + "Map"
	default:
		name = name + "s"
	}
	return name
}

func (b *SchemaBuilder) definitionNameForMapOfType(typeName string) string {
	var elementTypeName string
	var mapTypeName string
	if parts := strings.Split(typeName, "OR"); len(parts) > 1 {
		elementTypeName = lowerFirst(parts[0]) + "Or" + parts[1]
		b.noteUnionType(elementTypeName, parts[0], parts[1])
		mapTypeName = pluralize(lowerFirst(parts[0])) + "Or" + pluralize(parts[1])
	} else {
		elementTypeName = lowerFirst(typeName)
		mapTypeName = pluralize(elementTypeName)
	}
	b.noteMapType(mapTypeName, elementTypeName)
	return "#/definitions/" + mapTypeName
}

func (b *SchemaBuilder) updateSchemaFieldWithModelField(schemaField *jsonschema.Schema, modelField *SchemaObjectField) {
	if modelField.IsArray {
		itemSchema := &jsonschema.Schema{}
		switch modelField.Type {
		case "string":
			itemSchema.Type = jsonschema.NewStringOrStringArrayWithString("string")
		case "boolean":
			itemSchema.Type = jsonschema.NewStringOrStringArrayWithString("boolean")
		case "primitive":
			itemSchema.Ref = stringptr(b.definitionNameForType("Primitive"))
		default:
			itemSchema.Ref = stringptr(b.definitionNameForType(modelField.Type))
		}
		schemaField.Items = jsonschema.NewSchemaOrSchemaArrayWithSchema(itemSchema)
		schemaField.Type = jsonschema.NewStringOrStringArrayWithString("array")
		boolValue := true
		schemaField.UniqueItems = &boolValue
	} else if modelField.IsMap {
		schemaField.Ref = stringptr(b.definitionNameForMapOfType(modelField.Type))
	} else {
		switch modelField.Type {
		case "string":
			schemaField.Type = jsonschema.NewStringOrStringArrayWithString("string")
		case "boolean":
			schemaField.Type = jsonschema.NewStringOrStringArrayWithString("boolean")
		case "primitive":
			schemaField.Ref = stringptr(b.definitionNameForType("Primitive"))
		default:
			schemaField.Ref = stringptr(b.definitionNameForType(modelField.Type))
		}
	}
}

func (b *SchemaBuilder) BuildSchemaWithModel(modelObject *SchemaObject) *jsonschema.Schema {
	schema := &jsonschema.Schema{}
	schema.Type = jsonschema.NewStringOrStringArrayWithString("object")

	if modelObject.RequiredFields != nil && len(modelObject.RequiredFields) > 0 {
		arrayCopy := make([]string, len(modelObject.RequiredFields))
		copy(arrayCopy, modelObject.RequiredFields)
		schema.Required = &arrayCopy
	}

	schema.AdditionalProperties = jsonschema.NewSchemaOrBooleanWithBoolean(false)
	schema.Description = stringptr(modelObject.Description)

	// Handle fixed fields
	if modelObject.FixedFields != nil {
		newNamedSchemas := make([]*jsonschema.NamedSchema, 0)
		for _, modelField := range modelObject.FixedFields {
			schemaField := schema.PropertyWithName(modelField.Name)
			if schemaField == nil {
				schemaField = &jsonschema.Schema{}
				namedSchema := &jsonschema.NamedSchema{Name: modelField.Name, Value: schemaField}
				newNamedSchemas = append(newNamedSchemas, namedSchema)
			}
			b.updateSchemaFieldWithModelField(schemaField, &modelField)
		}
		for _, pair := range newNamedSchemas {
			if schema.Properties == nil {
				properties := make([]*jsonschema.NamedSchema, 0)
				schema.Properties = &properties
			}
			*(schema.Properties) = append(*(schema.Properties), pair)
		}
	} else {
		if schema.Properties != nil {
			fmt.Printf("SCHEMA SHOULD NOT HAVE PROPERTIES %s\n", modelObject.ID)
		}
	}

	// Handle patterned fields
	if modelObject.PatternedFields != nil {
		newNamedSchemas := make([]*jsonschema.NamedSchema, 0)
		for _, modelField := range modelObject.PatternedFields {
			schemaField := schema.PatternPropertyWithName(modelField.Name)
			if schemaField == nil {
				schemaField = &jsonschema.Schema{}
				nameRegex := "^[a-zA-Z0-9\\\\.\\\\-_]+$"
				if modelObject.Name == "Scopes Object" {
					nameRegex = "^"
				} else if modelObject.Name == "Headers Object" {
					nameRegex = "^[a-zA-Z0-9!#\\-\\$%&'\\*\\+\\\\\\.\\^_`\\|~]+"
				}
				propertyName := strings.Replace(modelField.Name, "{name}", nameRegex, -1)
				propertyName = strings.Replace(propertyName, "/{path}", "^/", -1)
				propertyName = strings.Replace(propertyName, "{expression}", "^", -1)
				propertyName = strings.Replace(propertyName, "{property}", "^", -1)
				namedSchema := &jsonschema.NamedSchema{Name: propertyName, Value: schemaField}
				newNamedSchemas = append(newNamedSchemas, namedSchema)
			}
			b.updateSchemaFieldWithModelField(schemaField, &modelField)
		}
		for _, pair := range newNamedSchemas {
			if schema.PatternProperties == nil {
				properties := make([]*jsonschema.NamedSchema, 0)
				schema.PatternProperties = &properties
			}
			*(schema.PatternProperties) = append(*(schema.PatternProperties), pair)
		}
	} else {
		if schema.PatternProperties != nil && !modelObject.Extendable {
			fmt.Printf("SCHEMA SHOULD NOT HAVE PATTERN PROPERTIES %s\n", modelObject.ID)
		}
	}

	// Handle specification extensions
	if modelObject.Extendable {
		schemaField := schema.PatternPropertyWithName("^x-")
		if schemaField != nil {
			schemaField.Ref = stringptr("#/definitions/specificationExtension")
		} else {
			schemaField = &jsonschema.Schema{}
			schemaField.Ref = stringptr("#/definitions/specificationExtension")
			namedSchema := &jsonschema.NamedSchema{Name: "^x-", Value: schemaField}
			if schema.PatternProperties == nil {
				properties := make([]*jsonschema.NamedSchema, 0)
				schema.PatternProperties = &properties
			}
			*(schema.PatternProperties) = append(*(schema.PatternProperties), namedSchema)
		}
	}

	return schema
}

// GenerateUnionTypes creates schema definitions for implied union types.
func (b *SchemaBuilder) GenerateUnionTypes(schema *jsonschema.Schema) {
	keys := make([]string, 0, len(b.UnionTypes))
	for key := range b.UnionTypes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		unionType := b.UnionTypes[key]
		objectSchema := schema.DefinitionWithName(unionType.Name)
		if objectSchema == nil {
			objectSchema = &jsonschema.Schema{}
			oneOf := make([]*jsonschema.Schema, 0)
			oneOf = append(oneOf, &jsonschema.Schema{Ref: stringptr("#/definitions/" + lowerFirst(unionType.ObjectType1))})
			oneOf = append(oneOf, &jsonschema.Schema{Ref: stringptr("#/definitions/" + lowerFirst(unionType.ObjectType2))})
			objectSchema.OneOf = &oneOf
			*schema.Definitions = append(*schema.Definitions, jsonschema.NewNamedSchema(unionType.Name, objectSchema))
		}
	}
}

// GenerateMapTypes creates schema definitions for implied map types.
func (b *SchemaBuilder) GenerateMapTypes(schema *jsonschema.Schema) {
	keys := make([]string, 0, len(b.MapTypes))
	for key := range b.MapTypes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		mapType := b.MapTypes[key]
		objectSchema := schema.DefinitionWithName(mapType.Name)
		if objectSchema == nil {
			objectSchema = &jsonschema.Schema{}
			objectSchema.Type = jsonschema.NewStringOrStringArrayWithString("object")
			additionalPropertiesSchema := &jsonschema.Schema{}
			if mapType.ObjectType == "string" {
				additionalPropertiesSchema.Type = jsonschema.NewStringOrStringArrayWithString("string")
			} else {
				additionalPropertiesSchema.Ref = stringptr("#/definitions/" + lowerFirst(mapType.ObjectType))
			}
			objectSchema.AdditionalProperties = jsonschema.NewSchemaOrBooleanWithSchema(additionalPropertiesSchema)
			*schema.Definitions = append(*schema.Definitions, jsonschema.NewNamedSchema(mapType.Name, objectSchema))
		}
	}
}

func stringptr(input string) *string {
	return &input
}

func int64ptr(input int64) *int64 {
	return &input
}

// ArrayOfSchema returns a schema for an array of schema-or-reference items.
func ArrayOfSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:     jsonschema.NewStringOrStringArrayWithString("array"),
		MinItems: int64ptr(1),
		Items:    jsonschema.NewSchemaOrSchemaArrayWithSchema(&jsonschema.Schema{Ref: stringptr("#/definitions/schemaOrReference")}),
	}
}
