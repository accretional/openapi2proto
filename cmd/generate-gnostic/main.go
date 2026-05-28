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
// Simplified to only generate OpenAPI v3 models for openapi2proto.

package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"

	"golang.org/x/tools/imports"

	"github.com/accretional/openapi2proto/internal/jsonschema"
)

// License is the software license applied to generated code.
const License = "" +
	"// Copyright 2020 Google LLC. All Rights Reserved.\n" +
	"//\n" +
	"// Licensed under the Apache License, Version 2.0 (the \"License\");\n" +
	"// you may not use this file except in compliance with the License.\n" +
	"// You may obtain a copy of the License at\n" +
	"//\n" +
	"//    http://www.apache.org/licenses/LICENSE-2.0\n" +
	"//\n" +
	"// Unless required by applicable law or agreed to in writing, software\n" +
	"// distributed under the License is distributed on an \"AS IS\" BASIS,\n" +
	"// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.\n" +
	"// See the License for the specific language governing permissions and\n" +
	"// limitations under the License.\n"

func protoOptions(directoryName string, packageName string) []ProtoOption {
	return []ProtoOption{
		{
			Name:  "java_multiple_files",
			Value: "true",
			Comment: "// This option lets the proto compiler generate Java code inside the package\n" +
				"// name (see below) instead of inside an outer class. It creates a simpler\n" +
				"// developer experience by reducing one-level of name nesting and be\n" +
				"// consistent with most programming languages that don't support outer classes.",
		},

		{
			Name:  "java_outer_classname",
			Value: "OpenAPIProto",
			Comment: "// The Java outer classname should be the filename in UpperCamelCase. This\n" +
				"// class is only used to hold proto descriptor, so developers don't need to\n" +
				"// work with it directly.",
		},

		{
			Name:    "java_package",
			Value:   "org." + packageName,
			Comment: "// The Java package name must be proto package name with proper prefix.",
		},

		{
			Name:  "objc_class_prefix",
			Value: "OAS",
			Comment: "// A reasonable prefix for the Objective-C symbols generated from the package.\n" +
				"// It should at a minimum be 3 characters long, all uppercase, and convention\n" +
				"// is to use an abbreviation of the package name. Something short, but\n" +
				"// hopefully unique enough to not conflict with things that may come along in\n" +
				"// the future. 'GPB' is reserved for the protocol buffer implementation itself.",
		},

		{
			Name:    "go_package",
			Value:   "github.com/accretional/openapi2proto/internal/openapiv3;openapi_v3",
			Comment: "// The Go package name.",
		},
	}
}

func generateOpenAPIModel(version string) error {
	var input string
	var filename string
	var protoPackageName string
	var directoryName string

	switch version {
	case "v3":
		input = "openapi-3.1.json"
		filename = "OpenAPIv3"
		protoPackageName = "openapi.v3"
		directoryName = "internal/openapiv3"
	default:
		return fmt.Errorf("unsupported OpenAPI version %s (this tool only supports v3)", version)
	}

	goPackageName := strings.Replace(protoPackageName, ".", "_", -1)

	projectRoot := "./"

	baseSchema, err := jsonschema.NewBaseSchema()
	if err != nil {
		return err
	}
	baseSchema.ResolveRefs()
	baseSchema.ResolveAllOfs()

	openapiSchema, err := jsonschema.NewSchemaFromFile(projectRoot + directoryName + "/" + input)
	if err != nil {
		return err
	}
	openapiSchema.ResolveRefs()
	openapiSchema.ResolveAllOfs()

	// build a simplified model of the types described by the schema
	cc := NewDomain(openapiSchema, version)
	// generators will map these patterns to the associated property names

	cc.TypeNameOverrides = map[string]string{
		"SpecificationExtension": "Any",
	}
	cc.PropertyNameOverrides = map[string]string{
		"PathItem":      "Path",
		"ResponseValue": "ResponseCode",
	}

	err = cc.Build()
	if err != nil {
		return err
	}

	if true {
		log.Printf("Type Model:\n%s", cc.Description())
	}

	// ensure that the target directory exists
	err = os.MkdirAll(projectRoot+directoryName, 0755)
	if err != nil {
		return err
	}

	// generate the protocol buffer description
	log.Printf("Generating protocol buffer description")
	proto := cc.generateProto(protoPackageName, License,
		protoOptions(directoryName, goPackageName), []string{"google/protobuf/any.proto"})
	protoFileName := projectRoot + directoryName + "/" + filename + ".proto"
	err = ioutil.WriteFile(protoFileName, []byte(proto), 0644)
	if err != nil {
		return err
	}

	// Use gnostic-models/compiler in generated code (not gnostic/compiler).
	packageImports := []string{
		"fmt",
		"go.yaml.in/yaml/v3",
		"strings",
		"regexp",
		"github.com/google/gnostic-models/compiler",
	}
	// generate the compiler
	log.Printf("Generating compiler support code")
	compiler := cc.GenerateCompiler(goPackageName, License, packageImports)
	goFileName := projectRoot + directoryName + "/" + filename + ".go"

	// format the compiler
	log.Printf("Formatting compiler support code")
	imports.LocalPrefix = "github.com/accretional/openapi2proto"
	data, err := imports.Process(goFileName, []byte(compiler), &imports.Options{
		TabWidth:  8,
		TabIndent: true,
		Comments:  true,
		Fragment:  true,
	})
	if err != nil {
		return err
	}

	return ioutil.WriteFile(goFileName, []byte(data), 0644)
}

func main() {
	openapiVersion := ""

	for i, arg := range os.Args {
		if i == 0 {
			continue // skip the tool name
		}
		if arg == "--v3" {
			openapiVersion = "v3"
		} else {
			fmt.Printf("Unknown option: %s.\n", arg)
			fmt.Printf("Usage: %s --v3\n", os.Args[0])
			os.Exit(1)
		}
	}

	if openapiVersion != "" {
		err := generateOpenAPIModel(openapiVersion)
		if err != nil {
			fmt.Printf("%+v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Printf("Usage: %s --v3\n", os.Args[0])
		os.Exit(1)
	}
}
