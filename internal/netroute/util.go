package netroute

import "strings"

func trimSpace(s string) string { return strings.TrimSpace(s) }

func lowerTrim(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func indexByte(s string, b byte) int { return strings.IndexByte(s, b) }

func splitSemicolon(s string) []string { return strings.Split(s, ";") }

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}
