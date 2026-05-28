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

// Forked from github.com/google/gnostic/generate-gnostic

package main

import (
	"fmt"
	"strings"

	"github.com/accretional/openapi2proto/internal/jsonschema"
)

/// Type Modeling

// TypeRequest models types encountered during model-building with no named schema.
type TypeRequest struct {
	Name         string
	PropertyName string
	Schema       *jsonschema.Schema
	OneOfWrapper bool
}

// NewTypeRequest creates a TypeRequest.
func NewTypeRequest(name string, propertyName string, schema *jsonschema.Schema) *TypeRequest {
	return &TypeRequest{Name: name, PropertyName: propertyName, Schema: schema}
}

// TypeProperty models type properties (fields).
type TypeProperty struct {
	Name             string
	Type             string
	StringEnumValues []string
	MapType          string
	Repeated         bool
	Pattern          string
	Implicit         bool
	Description      string
}

func (typeProperty *TypeProperty) description() string {
	result := ""
	if typeProperty.Description != "" {
		result += fmt.Sprintf("\t// %+s\n", typeProperty.Description)
	}
	if typeProperty.Repeated {
		result += fmt.Sprintf("\t%s %s repeated %s\n", typeProperty.Name, typeProperty.Type, typeProperty.Pattern)
	} else {
		result += fmt.Sprintf("\t%s %s %s \n", typeProperty.Name, typeProperty.Type, typeProperty.Pattern)
	}
	return result
}

// NewTypeProperty creates a TypeProperty.
func NewTypeProperty() *TypeProperty {
	return &TypeProperty{}
}

// NewTypePropertyWithNameAndType creates a TypeProperty.
func NewTypePropertyWithNameAndType(name string, typeName string) *TypeProperty {
	return &TypeProperty{Name: name, Type: typeName}
}

// NewTypePropertyWithNameTypeAndPattern creates a TypeProperty.
func NewTypePropertyWithNameTypeAndPattern(name string, typeName string, pattern string) *TypeProperty {
	return &TypeProperty{Name: name, Type: typeName, Pattern: pattern}
}

// FieldName returns the message field name for a property.
func (typeProperty *TypeProperty) FieldName() string {
	propertyName := typeProperty.Name
	if propertyName == "$ref" {
		return "XRef"
	}
	if propertyName == "$schema" {
		return "XSchema"
	}
	// Handle other $-prefixed JSON Schema keywords.
	if strings.HasPrefix(propertyName, "$") {
		return "X" + strings.Title(snakeCaseToCamelCase(propertyName[1:]))
	}
	return strings.Title(snakeCaseToCamelCase(propertyName))
}

// TypeModel models types.
type TypeModel struct {
	Name          string
	Properties    []*TypeProperty
	Required      []string
	OneOfWrapper  bool
	Open          bool
	OpenPatterns  []string
	IsStringArray bool
	IsItemArray   bool
	IsBlob        bool
	IsPair        bool
	PairValueType string
	Description   string
}

func (typeModel *TypeModel) addProperty(property *TypeProperty) {
	if typeModel.Properties == nil {
		typeModel.Properties = make([]*TypeProperty, 0)
	}
	typeModel.Properties = append(typeModel.Properties, property)
}

func (typeModel *TypeModel) description() string {
	result := ""
	if typeModel.Description != "" {
		result += fmt.Sprintf("// %+s\n", typeModel.Description)
	}
	var wrapperinfo string
	if typeModel.OneOfWrapper {
		wrapperinfo = " oneof wrapper"
	}
	result += fmt.Sprintf("%+s%s\n", typeModel.Name, wrapperinfo)
	for _, property := range typeModel.Properties {
		result += property.description()
	}
	return result
}

// NewTypeModel creates a TypeModel.
func NewTypeModel() *TypeModel {
	typeModel := &TypeModel{}
	typeModel.Properties = make([]*TypeProperty, 0)
	return typeModel
}

// IsRequired returns true if the named property is required.
func (typeModel *TypeModel) IsRequired(propertyName string) bool {
	for _, requiredName := range typeModel.Required {
		if requiredName == propertyName {
			return true
		}
	}
	return false
}
