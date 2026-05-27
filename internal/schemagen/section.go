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
	"regexp"
	"strings"
)

// Section models a section of the OpenAPI specification text document.
type Section struct {
	Level    int
	Text     string
	Title    string
	Children []*Section
}

// ReadSection reads a section of the OpenAPI Specification, recursively dividing it into subsections.
func ReadSection(text string, level int) (section *Section) {
	titlePattern := regexp.MustCompile("^" + strings.Repeat("#", level) + " .*$")
	subtitlePattern := regexp.MustCompile("^" + strings.Repeat("#", level+1) + " .*$")

	section = &Section{Level: level, Text: text}
	lines := strings.Split(text, "\n")
	subsection := ""
	for i, line := range lines {
		if i == 0 && titlePattern.Match([]byte(line)) {
			section.Title = line
		} else if subtitlePattern.Match([]byte(line)) {
			if len(subsection) != 0 {
				child := ReadSection(subsection, level+1)
				section.Children = append(section.Children, child)
			}
			subsection = line + "\n"
		} else {
			subsection += line + "\n"
		}
	}
	if len(section.Children) > 0 {
		child := ReadSection(subsection, level+1)
		section.Children = append(section.Children, child)
	}
	return
}

// Display recursively displays a section of the specification.
func (s *Section) Display(section string) {
	if len(s.Children) == 0 {
		// leaf section
	} else {
		for i, child := range s.Children {
			var subsection string
			if section == "" {
				subsection = fmt.Sprintf("%d", i)
			} else {
				subsection = fmt.Sprintf("%s.%d", section, i)
			}
			fmt.Printf("%-12s %s\n", subsection, child.NiceTitle())
			child.Display(subsection)
		}
	}
}

// NiceTitle returns the title text without leading "#" characters and without any anchor tags.
func (s *Section) NiceTitle() string {
	titleWithLinkPattern := regexp.MustCompile("^#+ <a .*</a>(.*)$")
	titlePattern := regexp.MustCompile("^#+ (.*)$")
	if matches := titleWithLinkPattern.FindSubmatch([]byte(s.Title)); matches != nil {
		return string(matches[1])
	} else if matches := titlePattern.FindSubmatch([]byte(s.Title)); matches != nil {
		return string(matches[1])
	}
	return ""
}

// FindChildByTitle finds the first child section whose NiceTitle matches the given title.
func (s *Section) FindChildByTitle(title string) *Section {
	for _, child := range s.Children {
		if child.NiceTitle() == title {
			return child
		}
	}
	return nil
}

// FindChildByTitlePrefix finds the first child section whose NiceTitle starts with the given prefix.
func (s *Section) FindChildByTitlePrefix(prefix string) *Section {
	for _, child := range s.Children {
		if strings.HasPrefix(child.NiceTitle(), prefix) {
			return child
		}
	}
	return nil
}
