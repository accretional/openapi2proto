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
	"os"
	"regexp"
	"strings"
)

// SchemaObjectField describes a field of a schema.
type SchemaObjectField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	IsArray     bool   `json:"is_array"`
	IsMap       bool   `json:"is_map"`
	Description string `json:"description"`
}

// SchemaObject describes a schema.
type SchemaObject struct {
	Name            string              `json:"name"`
	ID              string              `json:"id"`
	Description     string              `json:"description"`
	Extendable      bool                `json:"extendable"`
	RequiredFields  []string            `json:"required"`
	FixedFields     []SchemaObjectField `json:"fixed"`
	PatternedFields []SchemaObjectField `json:"patterned"`
}

// SchemaModel is a collection of schemas.
type SchemaModel struct {
	Objects []SchemaObject
}

func (m *SchemaModel) ObjectWithID(id string) *SchemaObject {
	for _, object := range m.Objects {
		if object.ID == id {
			return &object
		}
	}
	return nil
}

// SpecConfig holds version-specific parameters for parsing.
type SpecConfig struct {
	Version            string // "3.1" or "3.2"
	ObjectHeadingLevel int    // 4 for 3.1, 3 for 3.2
}

// DetectSpecConfig reads the first lines of a spec file to determine the version.
func DetectSpecConfig(filename string) (*SpecConfig, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	content := string(b)
	if strings.Contains(content[:min(len(content), 500)], "Version 3.2") {
		return &SpecConfig{Version: "3.2", ObjectHeadingLevel: 3}, nil
	}
	// Default to 3.1 format
	return &SpecConfig{Version: "3.1", ObjectHeadingLevel: 4}, nil
}

// deriveObjectID converts an object name to the ID used in JSON Schema definitions.
func deriveObjectID(name string) string {
	switch name {
	case "OpenAPI":
		return "oas"
	case "OAuth Flows":
		return "oauthFlows"
	case "OAuth Flow":
		return "oauthFlow"
	case "XML":
		return "xml"
	case "External Documentation":
		return "externalDocumentation"
	default:
		parts := strings.Fields(name)
		result := strings.ToLower(parts[0])
		for _, p := range parts[1:] {
			result += p
		}
		return result
	}
}

// findObjectsSection locates the section containing all Object definitions.
func findObjectsSection(doc *Section, cfg *SpecConfig) *Section {
	if cfg.Version == "3.2" {
		// 3.2: ## Objects and Fields → direct children are ### Object
		return doc.FindChildByTitlePrefix("Objects and Fields")
	}
	// 3.1: ## Specification → ### Schema → children are #### Object
	spec := doc.FindChildByTitle("Specification")
	if spec == nil {
		return nil
	}
	schema := spec.FindChildByTitle("Schema")
	if schema == nil {
		// try "Schema" as prefix in case of slight variation
		schema = spec.FindChildByTitlePrefix("Schema")
	}
	return schema
}

// collectFixedFields parses fixed fields from a section and its sub-sections (split table pattern).
// If sub-sections contain their own Fixed Fields tables, only parse those (not the parent text)
// to avoid double-counting.
func collectFixedFields(section *Section, schemaObject *SchemaObject) {
	hasSubTables := false
	for _, subChild := range section.Children {
		subTitle := subChild.NiceTitle()
		if strings.Contains(subTitle, "Fixed Fields") || strings.HasPrefix(subTitle, "Common Fixed Fields") {
			parseFixedFields(subChild.Text, schemaObject)
			hasSubTables = true
		}
	}
	if !hasSubTables {
		parseFixedFields(section.Text, schemaObject)
	}
}

// collectPatternedFields parses patterned fields from a section and its sub-sections.
func collectPatternedFields(section *Section, schemaObject *SchemaObject) {
	hasSubTables := false
	for _, subChild := range section.Children {
		subTitle := subChild.NiceTitle()
		if strings.Contains(subTitle, "Patterned Fields") {
			parsePatternedFields(subChild.Text, schemaObject)
			hasSubTables = true
		}
	}
	if !hasSubTables {
		parsePatternedFields(section.Text, schemaObject)
	}
}

// NewSchemaModel parses a spec markdown file and returns a SchemaModel.
func NewSchemaModel(filename string, cfg *SpecConfig) (*SchemaModel, error) {
	b, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	document := ReadSection(string(b), 1)

	objectsSection := findObjectsSection(document, cfg)
	if objectsSection == nil {
		return nil, fmt.Errorf("unable to find objects section; has the source document structure changed?")
	}

	// Match headings like "OpenAPI Object" (NiceTitle strips leading #'s)
	objectPattern := regexp.MustCompile(`^(.+) Object$`)

	schemaObjects := make([]SchemaObject, 0)
	for _, section := range objectsSection.Children {
		title := section.NiceTitle()
		if matches := objectPattern.FindStringSubmatch(title); matches != nil {
			objectName := matches[1]
			id := deriveObjectID(objectName)

			schemaObject := SchemaObject{
				Name:           title,
				ID:             id,
				RequiredFields: nil,
			}

			// Extract description from the first child or from text before any child
			if len(section.Children) > 0 {
				// Use text between the heading and the first child heading
				description := section.Children[0].Text
				description = removeMarkdownLinks(description)
				description = strings.Trim(description, " \t\n")
				description = strings.Replace(description, "\n", " ", -1)
				schemaObject.Description = description
			}

			// Is the object extendable?
			if strings.Contains(section.Text, "Specification Extensions") {
				schemaObject.Extendable = true
			}

			// Look for fixed fields (handles both single tables and split sub-tables)
			for _, child := range section.Children {
				childTitle := child.NiceTitle()
				if childTitle == "Fixed Fields" || strings.HasPrefix(childTitle, "Fixed Fields") {
					collectFixedFields(child, &schemaObject)
				}
			}

			// Look for patterned fields
			for _, child := range section.Children {
				childTitle := child.NiceTitle()
				if childTitle == "Patterned Fields" || strings.HasPrefix(childTitle, "Patterned Fields") {
					collectPatternedFields(child, &schemaObject)
				}
			}

			schemaObjects = append(schemaObjects, schemaObject)
		}
	}

	return &SchemaModel{Objects: schemaObjects}, nil
}
