package compiler

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// yamlToJSON converts a Forge pipeline YAML document to JSON bytes
// so the rest of the compiler pipeline can process it normally.
func yamlToJSON(data []byte) ([]byte, error) {
	p := &yamlParser{lines: splitLines(string(data)), pos: 0}
	val, err := p.parseValue(0)
	if err != nil {
		return nil, fmt.Errorf("YAML parse error: %w", err)
	}
	return json.Marshal(val)
}

func splitLines(src string) []string {

	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// indent returns the number of leading spaces on a line.
func indent(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

// isComment returns true for blank lines and comment lines.
func isComment(line string) bool {
	t := strings.TrimSpace(line)
	return t == "" || strings.HasPrefix(t, "#")
}

type yamlParser struct {
	lines []string
	pos   int
}

// peek returns the current non-comment line without consuming it.
// Returns ("", false) at EOF.
func (p *yamlParser) peek() (string, bool) {
	for p.pos < len(p.lines) {
		if !isComment(p.lines[p.pos]) {
			return p.lines[p.pos], true
		}
		p.pos++
	}
	return "", false
}

// parseValue is the top-level entry point. It dispatches to the right
// parser based on the current indentation level.
func (p *yamlParser) parseValue(baseIndent int) (any, error) {
	line, ok := p.peek()
	if !ok {
		return nil, nil
	}

	ind := indent(line)
	if ind < baseIndent {
		return nil, nil
	}

	trimmed := strings.TrimSpace(line)

	if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
		return p.parseSequence(ind)
	}

	if strings.Contains(trimmed, ":") {
		return p.parseMapping(ind)
	}

	return nil, fmt.Errorf("unexpected line %q", line)
}

// parseMapping reads a block mapping (key: value pairs at the same indent).
func (p *yamlParser) parseMapping(mapIndent int) (map[string]any, error) {
	result := map[string]any{}

	for {
		line, ok := p.peek()
		if !ok {
			break
		}
		if indent(line) < mapIndent {
			break
		}
		if indent(line) > mapIndent {
			break
		}

		p.pos++
		trimmed := strings.TrimSpace(line)

		before, after, ok0 := strings.Cut(trimmed, ":")
		if !ok0 {
			continue
		}
		key := unquoteKey(strings.TrimSpace(before))
		rest := strings.TrimSpace(after)

		var val any
		var err error

		switch {
		case rest == "|":
			val = p.parseLiteralBlock(mapIndent + 2)

		case rest == "":
			next, ok := p.peek()
			if !ok {
				val = nil
			} else if indent(next) > mapIndent {
				nextTrimmed := strings.TrimSpace(next)
				if strings.HasPrefix(nextTrimmed, "- ") || nextTrimmed == "-" {
					val, err = p.parseSequence(indent(next))
				} else {
					val, err = p.parseMapping(indent(next))
				}
			} else {
				val = nil
			}

		default:
			val = parseScalar(rest)
		}

		if err != nil {
			return nil, err
		}
		result[key] = val
	}
	return result, nil
}

// parseSequence reads a block sequence (list of "- item" at the same indent).
func (p *yamlParser) parseSequence(seqIndent int) ([]any, error) {
	var result []any

	for {
		line, ok := p.peek()
		if !ok {
			break
		}
		if indent(line) < seqIndent {
			break
		}

		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
			break
		}
		p.pos++

		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))

		var item any
		var err error

		if rest == "" {

			next, ok := p.peek()
			if ok && indent(next) > seqIndent {
				nextTrimmed := strings.TrimSpace(next)
				if strings.HasPrefix(nextTrimmed, "- ") {
					item, err = p.parseSequence(indent(next))
				} else {
					item, err = p.parseMapping(indent(next))
				}
			}
		} else if strings.Contains(rest, ":") && !strings.HasPrefix(rest, "{") {

			inlineMap := map[string]any{}
			colonIdx := strings.Index(rest, ":")
			k := unquoteKey(strings.TrimSpace(rest[:colonIdx]))
			v := strings.TrimSpace(rest[colonIdx+1:])
			inlineMap[k] = parseScalar(v)

			for {
				next, ok := p.peek()
				if !ok || indent(next) <= seqIndent {
					break
				}
				nextTrimmed := strings.TrimSpace(next)
				if strings.HasPrefix(nextTrimmed, "- ") {
					break
				}
				p.pos++

				before, after, ok0 := strings.Cut(nextTrimmed, ":")
				if !ok0 {
					continue
				}
				fk := unquoteKey(strings.TrimSpace(before))
				fv := strings.TrimSpace(after)

				if fv == "|" {
					inlineMap[fk] = p.parseLiteralBlock(indent(next) + 2)
				} else if fv == "" {
					fn, fok := p.peek()
					if fok && indent(fn) > indent(next) {
						fnTrimmed := strings.TrimSpace(fn)
						if strings.HasPrefix(fnTrimmed, "- ") || fnTrimmed == "-" {
							inlineMap[fk], err = p.parseSequence(indent(fn))
						} else {
							inlineMap[fk], err = p.parseMapping(indent(fn))
						}
					}
				} else {
					inlineMap[fk] = parseScalar(fv)
				}
				if err != nil {
					return nil, err
				}
			}
			item = inlineMap
		} else {
			item = parseScalar(rest)
		}

		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, nil
}

// parseLiteralBlock reads a literal block scalar (the content after "key: |").
// Strips the common leading indentation from all content lines.
func (p *yamlParser) parseLiteralBlock(contentIndent int) string {
	var lines []string
	for {
		if p.pos >= len(p.lines) {
			break
		}
		line := p.lines[p.pos]
		if strings.TrimSpace(line) == "" {
			lines = append(lines, "")
			p.pos++
			continue
		}
		if indent(line) < contentIndent {
			break
		}

		stripped := line
		if len(line) >= contentIndent {
			stripped = line[contentIndent:]
		}
		lines = append(lines, stripped)
		p.pos++
	}

	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// unquoteKey strips a single layer of matching quote characters from a
// mapping key, the same way parseScalar does for values. Mapping keys
// are used verbatim as map[string]any keys rather than passed through
// parseScalar (a key must always stay a string, never become a bool,
// number, or nil the way a quoted value legitimately can't either),
// but without this a key written the natural way — "1.0.0": ... in a
// version table, for instance — would keep its literal quote
// characters and silently fail every lookup by its intended,
// unquoted name.
func unquoteKey(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// parseScalar converts a YAML scalar string to its Go representation.
func parseScalar(s string) any {
	s = stripComment(s)
	if s == "" || s == "~" || s == "null" {
		return nil
	}
	if s == "true" || s == "yes" || s == "on" {
		return true
	}
	if s == "false" || s == "no" || s == "off" {
		return false
	}

	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return parseInlineSeq(s[1 : len(s)-1])
	}

	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}

	// Try to parse as int
	if i, err := strconv.Atoi(s); err == nil {
		return i
	}

	// Try to parse as float
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}

	return s
}

func stripComment(s string) string {
	if s == "" {
		return ""
	}
	// If it starts with a quote, the comment must be after the matching closing quote.
	if s[0] == '"' || s[0] == '\'' {
		quote := s[0]
		escaped := false
		for i := 1; i < len(s); i++ {
			if escaped {
				escaped = false
				continue
			}
			if s[i] == '\\' && quote == '"' {
				escaped = true
				continue
			}
			if s[i] == quote {
				// Potential end of quote. In YAML, '' inside '' is an escaped '.
				if quote == '\'' && i+1 < len(s) && s[i+1] == '\'' {
					i++ // skip next '
					continue
				}
				// Found end of quote. Strip everything after it that starts with #
				after := s[i+1:]
				if found := strings.Contains(after, "#"); found {
					return s[:i+1]
				}
				return s
			}
		}
		return s
	}

	// Unquoted. Strip # if it's at the start or preceded by whitespace.
	for i := 0; i < len(s); i++ {
		if s[i] == '#' {
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return s
}

// parseInlineSeq splits "[a, b, c]" content into a []interface{}.
func parseInlineSeq(inner string) []any {
	if strings.TrimSpace(inner) == "" {
		return []any{}
	}
	parts := strings.Split(inner, ",")
	result := make([]any, 0, len(parts))
	for _, p := range parts {
		result = append(result, parseScalar(strings.TrimSpace(p)))
	}
	return result
}
