package generator

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
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
	// Protobuf forbids two fields in a message from sharing a JSON name. protoc
	// derives the JSON name by lower-camel-casing the field name, so distinct
	// snake_case names like "companion_clicks_1" and "companion_clicks1" both
	// map to "companionClicks1" and collide (e.g. the AdButler spec). Track the
	// derived JSON name (prefixed with "json:" to share the seen map) and append
	// a numeric suffix until both the field name and its JSON name are unique.
	candidate := base
	for {
		jsonKey := "json:" + protoJSONName(candidate)
		if seen[candidate] == 0 && seen[jsonKey] == 0 {
			seen[candidate] = 1
			seen[jsonKey] = 1
			return candidate
		}
		n := seen[base] + 1
		seen[base] = n
		candidate = base + "_" + strconv.Itoa(n)
	}
}

// protoJSONName replicates protoc's default JSON name derivation: the
// underscore-delimited field name is lower-camel-cased (each part after the
// first is capitalized, underscores removed).
func protoJSONName(name string) string {
	var b strings.Builder
	upperNext := false
	for _, r := range name {
		if r == '_' {
			upperNext = true
			continue
		}
		if upperNext {
			b.WriteRune(unicode.ToUpper(r))
			upperNext = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
