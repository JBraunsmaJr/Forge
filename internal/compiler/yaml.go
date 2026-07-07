// Package compiler — YAML pipeline parser.
//
// Forge pipeline files may be written in YAML instead of JSON.
// This file converts them to JSON so the existing compiler can process them.
//
// We implement our own converter rather than importing gopkg.in/yaml.v3 because
// that domain is unavailable in this build environment. The converter handles
// exactly the YAML subset needed for Forge pipelines:
//
//   - Block mappings and sequences
//   - Quoted and unquoted scalar values
//   - Literal block scalars (|) — used for multi-line `run:` scripts
//   - Inline sequences ([a, b, c])
//   - Boolean scalars (true/false)
//   - Comments (#)
//
// General-purpose YAML features (anchors, tags, multi-document, etc.) are
// NOT supported and will produce a parse error or be silently ignored.
package compiler

import (
	"encoding/json"
	"fmt"
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

// ── Tokeniser ─────────────────────────────────────────────────────────────────

func splitLines(src string) []string {
	// Normalise line endings and strip trailing blank lines.
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

// ── Parser ────────────────────────────────────────────────────────────────────

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
func (p *yamlParser) parseValue(baseIndent int) (interface{}, error) {
	line, ok := p.peek()
	if !ok {
		return nil, nil
	}

	ind := indent(line)
	if ind < baseIndent {
		return nil, nil // caller's block ended
	}

	trimmed := strings.TrimSpace(line)

	// Block sequence: lines starting with "- "
	if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
		return p.parseSequence(ind)
	}

	// Block mapping: lines containing ":"
	if strings.Contains(trimmed, ":") {
		return p.parseMapping(ind)
	}

	return nil, fmt.Errorf("unexpected line %q", line)
}

// parseMapping reads a block mapping (key: value pairs at the same indent).
func (p *yamlParser) parseMapping(mapIndent int) (map[string]interface{}, error) {
	result := map[string]interface{}{}

	for {
		line, ok := p.peek()
		if !ok {
			break
		}
		if indent(line) < mapIndent {
			break // block ended
		}
		if indent(line) > mapIndent {
			break // unexpected deeper line — will cause parent to handle
		}

		p.pos++
		trimmed := strings.TrimSpace(line)

		// Parse "key: rest"
		colonIdx := strings.Index(trimmed, ":")
		if colonIdx < 0 {
			continue // skip malformed lines
		}
		key := strings.TrimSpace(trimmed[:colonIdx])
		rest := strings.TrimSpace(trimmed[colonIdx+1:])

		var val interface{}
		var err error

		switch {
		case rest == "|": // literal block scalar — multi-line string
			val = p.parseLiteralBlock(mapIndent + 2)

		case rest == "": // value is on next line(s) — nested map or sequence
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

		default: // value is on the same line
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
func (p *yamlParser) parseSequence(seqIndent int) ([]interface{}, error) {
	var result []interface{}

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

		var item interface{}
		var err error

		if rest == "" {
			// Value is on following lines (nested map/sequence)
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
			// Inline map in sequence: "- key: value" → first key:value pair,
			// then remaining pairs are on the following lines at deeper indent.
			inlineMap := map[string]interface{}{}
			colonIdx := strings.Index(rest, ":")
			k := strings.TrimSpace(rest[:colonIdx])
			v := strings.TrimSpace(rest[colonIdx+1:])
			inlineMap[k] = parseScalar(v)

			// Collect remaining fields at a deeper indent.
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

				ci := strings.Index(nextTrimmed, ":")
				if ci < 0 {
					continue
				}
				fk := strings.TrimSpace(nextTrimmed[:ci])
				fv := strings.TrimSpace(nextTrimmed[ci+1:])

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
		// Strip exactly contentIndent spaces of indentation.
		stripped := line
		if len(line) >= contentIndent {
			stripped = line[contentIndent:]
		}
		lines = append(lines, stripped)
		p.pos++
	}

	// Trim trailing blank lines, then add a single trailing newline.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// ── Scalar parsing ────────────────────────────────────────────────────────────

// parseScalar converts a YAML scalar string to its Go representation.
func parseScalar(s string) interface{} {
	if s == "" || s == "~" || s == "null" {
		return nil
	}
	if s == "true" || s == "yes" || s == "on" {
		return true
	}
	if s == "false" || s == "no" || s == "off" {
		return false
	}

	// Inline sequence: [a, b, c]
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		return parseInlineSeq(s[1 : len(s)-1])
	}

	// Quoted string: "value" or 'value'
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}

	return s // unquoted string
}

// parseInlineSeq splits "[a, b, c]" content into a []interface{}.
func parseInlineSeq(inner string) []interface{} {
	if strings.TrimSpace(inner) == "" {
		return []interface{}{}
	}
	parts := strings.Split(inner, ",")
	result := make([]interface{}, 0, len(parts))
	for _, p := range parts {
		result = append(result, parseScalar(strings.TrimSpace(p)))
	}
	return result
}
