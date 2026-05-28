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
	"log"
	"regexp"
	"strings"
)

// stripLink removes a link from a string, leaving only the text that follows it.
func stripLink(input string) string {
	stringWithLinkPattern := regexp.MustCompile("^(.*)$")
	stringWithAnchorPattern := regexp.MustCompile("^<a .*</a>(.*)$")
	if matches := stringWithAnchorPattern.FindSubmatch([]byte(input)); matches != nil {
		return string(matches[1])
	} else if matches := stringWithLinkPattern.FindSubmatch([]byte(input)); matches != nil {
		return string(matches[1])
	}
	return input
}

// removeMarkdownLinks replaces markdown links with their link text.
func removeMarkdownLinks(input string) string {
	markdownLink := regexp.MustCompile("\\[([^\\]\\[]*)\\]\\(([^\\)]*)\\)")
	return string(markdownLink.ReplaceAll([]byte(input), []byte("$1")))
}

// trimPipeParts handles pipe-prefixed table rows by removing empty leading/trailing parts.
func trimPipeParts(parts []string) []string {
	for len(parts) > 0 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	for len(parts) > 0 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// parseFixedFields extracts the fixed fields from a table in a section.
func parseFixedFields(input string, schemaObject *SchemaObject) {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		// replace escaped bars with "OR", assuming these are used to describe union types
		line = strings.Replace(line, " \\| ", " OR ", -1)

		parts := strings.Split(line, "|")
		parts = trimPipeParts(parts)

		if len(parts) > 1 {
			fieldName := strings.TrimSpace(stripLink(strings.TrimSpace(parts[0])))
			// skip header and separator rows
			if fieldName == "Field Name" || strings.HasPrefix(fieldName, "---") || strings.HasPrefix(fieldName, "----") {
				continue
			}
			// skip rows from non-field tables (descriptive tables with backtick-wrapped
			// names, markdown links, or other non-identifier characters).
			// Valid field names are camelCase identifiers optionally prefixed with $.
			validFieldName := regexp.MustCompile(`^[$]?[a-zA-Z][a-zA-Z0-9]*$`)
			if !validFieldName.MatchString(fieldName) {
				continue
			}

			if len(parts) == 3 || len(parts) == 4 {
				// expected column count
			} else {
				log.Printf("ERROR: %+v", parts)
			}

			typeName := parts[1]
			typeName = strings.Replace(typeName, "{expression}", "Expression", -1)
			typeName = strings.Trim(typeName, " ")
			typeName = strings.Replace(typeName, "`", "", -1)
			typeName = removeMarkdownLinks(typeName)
			typeName = strings.Replace(typeName, " ", "", -1)
			typeName = strings.Replace(typeName, "Object", "", -1)
			isArray := false
			if len(typeName) > 0 && typeName[0] == '[' && typeName[len(typeName)-1] == ']' {
				typeName = typeName[1 : len(typeName)-1]
				isArray = true
			}
			isMap := false
			mapPattern := regexp.MustCompile("^Mapstring,\\[(.*)\\]$")
			if matches := mapPattern.FindSubmatch([]byte(typeName)); matches != nil {
				typeName = string(matches[1])
				isMap = true
			} else {
				mapPattern2 := regexp.MustCompile("^Map\\[string,(.+)\\]$")
				if matches := mapPattern2.FindSubmatch([]byte(typeName)); matches != nil {
					typeName = string(matches[1])
					isMap = true
				}
			}
			description := strings.Trim(parts[len(parts)-1], " ")
			description = removeMarkdownLinks(description)
			description = strings.Replace(description, "\n", " ", -1)

			requiredLabel1 := "**Required.** "
			requiredLabel2 := "**REQUIRED**."
			if strings.Contains(description, requiredLabel1) ||
				strings.Contains(description, requiredLabel2) {
				valid := true
				if len(parts) == 4 {
					validity := parts[2]
					if strings.Contains(validity, "Any") {
						valid = true
					} else {
						valid = false
					}
				}
				if valid {
					schemaObject.RequiredFields = append(schemaObject.RequiredFields, fieldName)
				}
				description = strings.Replace(description, requiredLabel1, "", -1)
				description = strings.Replace(description, requiredLabel2, "", -1)
			}
			schemaField := SchemaObjectField{
				Name:        fieldName,
				Type:        typeName,
				IsArray:     isArray,
				IsMap:       isMap,
				Description: description,
			}
			schemaObject.FixedFields = append(schemaObject.FixedFields, schemaField)
		}
	}
}

// parsePatternedFields extracts the patterned fields from a table in a section.
func parsePatternedFields(input string, schemaObject *SchemaObject) {
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		line = strings.Replace(line, " \\| ", " OR ", -1)

		parts := strings.Split(line, "|")
		parts = trimPipeParts(parts)

		if len(parts) > 1 {
			fieldName := strings.TrimSpace(stripLink(strings.TrimSpace(parts[0])))
			fieldName = removeMarkdownLinks(fieldName)
			if fieldName == "HTTP Status Code" {
				fieldName = "^([0-9X]{3})$"
			}
			// skip header and separator rows
			if fieldName == "Field Pattern" || strings.HasPrefix(fieldName, "---") || strings.HasPrefix(fieldName, "----") {
				continue
			}

			typeName := parts[1]
			typeName = strings.Trim(typeName, " ")
			typeName = strings.Replace(typeName, "`", "", -1)
			typeName = removeMarkdownLinks(typeName)
			typeName = strings.Replace(typeName, " ", "", -1)
			typeName = strings.Replace(typeName, "Object", "", -1)
			typeName = strings.Replace(typeName, "{expression}", "Expression", -1)
			isArray := false
			if len(typeName) > 0 && typeName[0] == '[' && typeName[len(typeName)-1] == ']' {
				typeName = typeName[1 : len(typeName)-1]
				isArray = true
			}
			isMap := false
			mapPattern := regexp.MustCompile("^Mapstring,\\[(.*)\\]$")
			if matches := mapPattern.FindSubmatch([]byte(typeName)); matches != nil {
				typeName = string(matches[1])
				isMap = true
			}
			description := strings.Trim(parts[len(parts)-1], " ")
			description = removeMarkdownLinks(description)
			description = strings.Replace(description, "\n", " ", -1)

			schemaField := SchemaObjectField{
				Name:        fieldName,
				Type:        typeName,
				IsArray:     isArray,
				IsMap:       isMap,
				Description: description,
			}
			schemaObject.PatternedFields = append(schemaObject.PatternedFields, schemaField)
		}
	}
}
