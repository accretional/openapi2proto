package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/accretional/openapi2proto/generator"
)

func main() {
	input := flag.String("input", "", "Path to OpenAPI v3 spec (JSON or YAML)")
	output := flag.String("output", "", "Output .proto file path (default: stdout)")
	pkg := flag.String("package", "", "Proto package name (default: derived from input filename)")
	goPkg := flag.String("go_package", "", "Go package option (default: derived from package)")
	grouping := flag.String("grouping", "tag", "Service grouping strategy: tag or single")
	noHTTP := flag.Bool("no_http", false, "Disable HTTP annotations")
	serviceOut := flag.String("service_out", "", "Output path for generated Go service file (requires -go_module)")
	goModule := flag.String("go_module", "", "Go module path of the consuming project (e.g. github.com/org/repo)")
	runtimeImport := flag.String("runtime_import", "", "Go import path for the runtime package (default: github.com/accretional/openapi2proto/runtime)")
	flag.Parse()

	if *input == "" {
		fmt.Fprintln(os.Stderr, "error: -input is required")
		flag.Usage()
		os.Exit(1)
	}

	data, err := os.ReadFile(*input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading input: %v\n", err)
		os.Exit(1)
	}

	doc, err := generator.DecodeOpenAPIFromBytes(data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing OpenAPI spec: %v\n", err)
		os.Exit(1)
	}

	cfg := generator.Config{
		PackageName:         *pkg,
		GoPackage:           *goPkg,
		ServiceGrouping:     *grouping,
		EmitHTTPAnnotations: !*noHTTP,
		HasHTTPPreference:   *noHTTP,
	}

	proto, err := generator.Generate(*input, doc, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error generating proto: %v\n", err)
		os.Exit(1)
	}

	if *output == "" {
		os.Stdout.Write(proto)
	} else {
		if err := writeFile(*output, proto); err != nil {
			fmt.Fprintf(os.Stderr, "error writing proto: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", *output)
	}

	// Optionally emit Go service implementation.
	if *serviceOut != "" {
		if *goModule == "" {
			fmt.Fprintln(os.Stderr, "error: -go_module is required with -service_out")
			os.Exit(1)
		}
		// Derive pb sub-path from go_package: the part before ";" is the import path.
		pbSubPath := pbSubPathFromGoPackage(*goPkg, *output)
		svcCode, err := generator.GenerateGoService(*input, doc, cfg, *goModule, pbSubPath, *runtimeImport)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error generating service: %v\n", err)
			os.Exit(1)
		}
		if err := writeFile(*serviceOut, svcCode); err != nil {
			fmt.Fprintf(os.Stderr, "error writing service: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", *serviceOut)
	}
}

// pbSubPathFromGoPackage derives the pb import sub-path.
// If go_package is "cloudflare/zones;cloudflarezones" and output is
// "proto/cloudflare/zones/cloudflare_zones.proto", the pb path is
// "pb/cloudflare/zones".
func pbSubPathFromGoPackage(goPkg, protoOutput string) string {
	// Use the part of go_package before ";".
	pkgPath := goPkg
	if idx := strings.Index(pkgPath, ";"); idx >= 0 {
		pkgPath = pkgPath[:idx]
	}
	if pkgPath != "" {
		return "pb/" + pkgPath
	}
	// Fall back to deriving from the proto output path.
	if protoOutput != "" {
		dir := getDir(protoOutput)
		// Replace leading "proto/" with "pb/".
		if strings.HasPrefix(dir, "proto/") {
			return "pb/" + dir[6:]
		}
	}
	return "pb"
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(getDir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func getDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return "."
}
