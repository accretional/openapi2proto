package generator

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	separatorRE    = regexp.MustCompile(`[^a-zA-Z0-9]+`)
	camelBreakRE   = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	protoKeywordRE = regexp.MustCompile(`^(syntax|import|package|option|message|enum|service|rpc|returns|map|repeated|oneof|reserved|public|weak|stream|extensions|to|max|group)$`)
)

func words(input string) []string {
	if input == "" {
		return nil
	}
	s := camelBreakRE.ReplaceAllString(input, `$1 $2`)
	s = separatorRE.ReplaceAllString(s, " ")
	parts := strings.Fields(s)
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}
	return parts
}

func toCamel(input string) string {
	parts := words(input)
	if len(parts) == 0 {
		return "Generated"
	}
	var out strings.Builder
	for _, part := range parts {
		lower := strings.ToLower(part)
		out.WriteString(strings.ToUpper(lower[:1]))
		if len(lower) > 1 {
			out.WriteString(lower[1:])
		}
	}
	result := out.String()
	if result == "" {
		return "Generated"
	}
	if result[0] >= '0' && result[0] <= '9' {
		return "X" + result
	}
	return result
}

func toSnake(input string) string {
	parts := words(input)
	if len(parts) == 0 {
		return "generated"
	}
	for i, part := range parts {
		parts[i] = strings.ToLower(part)
	}
	result := strings.Join(parts, "_")
	if result[0] >= '0' && result[0] <= '9' {
		result = "n_" + result
	}
	if protoKeywordRE.MatchString(result) {
		result += "_field"
	}
	return result
}

// defaultJSONName reproduces protoc's own json_name derivation: strip
// underscores, capitalizing whatever follows one. protojson accepts a field
// under its proto name OR this derived name, so a property already reachable
// by one of the two needs no explicit annotation.
func defaultJSONName(name string) string {
	var out strings.Builder
	upperNext := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' {
			upperNext = true
			continue
		}
		if upperNext {
			out.WriteString(strings.ToUpper(string(c)))
			upperNext = false
			continue
		}
		out.WriteByte(c)
	}
	return out.String()
}

// toScreamingSnake converts an identifier to UPPER_SNAKE_CASE, the proto
// convention for enum value names. Used both for enum value identifiers and as
// the per-enum value-name prefix (proto3 enum values share the enclosing scope,
// so values are prefixed by their enum type name to stay unique within a file).
func toScreamingSnake(input string) string {
	parts := words(input)
	if len(parts) == 0 {
		return ""
	}
	for i, part := range parts {
		parts[i] = strings.ToUpper(part)
	}
	return strings.Join(parts, "_")
}

func sanitizePackageName(input string) string {
	parts := words(input)
	if len(parts) == 0 {
		return "openapi.generated"
	}
	return joinPackageParts(parts)
}

func sanitizeDottedPackageName(input string) string {
	rawParts := strings.Split(input, ".")
	parts := make([]string, 0, len(rawParts))
	for _, raw := range rawParts {
		parts = append(parts, words(raw)...)
	}
	if len(parts) == 0 {
		return "openapi.generated"
	}
	return joinPackageParts(parts)
}

func joinPackageParts(parts []string) string {
	for i, part := range parts {
		parts[i] = strings.ToLower(part)
		if parts[i] == "" {
			parts[i] = "generated"
		}
		if parts[i][0] >= '0' && parts[i][0] <= '9' {
			parts[i] = "v" + parts[i]
		}
	}
	return strings.Join(parts, ".")
}

func unique(base string, seen map[string]int) string {
	if base == "" {
		base = "Generated"
	}
	if n := seen[base]; n == 0 {
		seen[base] = 1
		return base
	}
	n := seen[base] + 1
	seen[base] = n
	return base + strconv.Itoa(n)
}

func uniqueField(base string, seen map[string]int) string {
	if base == "" {
		base = "field"
	}
	if n := seen[base]; n == 0 {
		seen[base] = 1
		return base
	}
	n := seen[base] + 1
	seen[base] = n
	return base + "_" + strconv.Itoa(n)
}
