package main

import (
	"bytes"
	"errors"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

var errInvalidTOML = errors.New("invalid TOML settings")

type tomlExpression struct {
	kind       unstable.Kind
	key        []string
	comment    string
	start, end int
}

// parseTOML validates the document with the stable decoder before using the
// unstable parser for decoded keys and source positions. The AST parser alone
// deliberately permits duplicate keys, which must never be accepted here.
func parseTOML(data []byte) ([]tomlExpression, error) {
	if err := validateTOML(data); err != nil {
		return nil, err
	}

	var parser unstable.Parser
	parser.KeepComments = true
	parser.Reset(data)
	var expressions []tomlExpression
	for parser.NextExpression() {
		for node := parser.Expression(); node.Valid(); node = node.Next() {
			expression := tomlExpression{kind: node.Kind}
			switch node.Kind {
			case unstable.Table, unstable.ArrayTable, unstable.KeyValue:
				expression.key = tomlNodeKey(node)
				expression.start = tomlNodeLineStart(data, node)
			case unstable.Comment:
				expression.start = bytes.LastIndexByte(data[:node.Raw.Offset], '\n') + 1
				if len(bytes.TrimSpace(data[expression.start:node.Raw.Offset])) != 0 {
					continue
				}
				expression.comment = string(parser.Raw(node.Raw))
			default:
				continue
			}
			expressions = append(expressions, expression)
		}
	}
	if parser.Error() != nil {
		return nil, errInvalidTOML
	}
	for i := range expressions {
		next := len(data)
		if i+1 < len(expressions) {
			next = expressions[i+1].start
		}
		expressions[i].end = tomlLastNonBlankLineEnd(data, expressions[i].start, next)
	}
	return expressions, nil
}

func validateTOML(data []byte) error {
	var decoded map[string]any
	if err := toml.Unmarshal(data, &decoded); err != nil {
		return errInvalidTOML
	}
	return nil
}

func tomlNodeKey(node *unstable.Node) []string {
	key := node.Key()
	var parts []string
	for key.Next() {
		parts = append(parts, string(key.Node().Data))
	}
	return parts
}

// KeyValue and Table nodes do not currently have Raw ranges. Their first key
// does, and the next AST expression defines the end of this expression.
func tomlNodeLineStart(data []byte, node *unstable.Node) int {
	key := node.Key()
	key.Next()
	return bytes.LastIndexByte(data[:key.Node().Raw.Offset], '\n') + 1
}

func tomlLastNonBlankLineEnd(data []byte, start, end int) int {
	for end > start {
		lineStart := start + bytes.LastIndexByte(data[start:end-1], '\n') + 1
		line := bytes.TrimSpace(data[lineStart:end])
		if len(line) > 0 {
			return end
		}
		end = lineStart
	}
	return start
}

// codexTelemetryRange returns the byte range occupied by Codex's two legacy
// OpenTelemetry tables. It recognizes only the small, safe shape that setup
// can replace without reformatting unrelated user configuration.
func codexTelemetryRange(data []byte) (start, end int, ok bool) {
	expressions, err := parseTOML(data)
	if err != nil {
		return 0, 0, false
	}

	seen := make(map[string]bool, 2)
	active := ""
	first, last := -1, -1
	for _, expression := range expressions {
		switch expression.kind {
		case unstable.Table, unstable.ArrayTable:
			if expression.kind != unstable.Table || !isCodexTelemetryTable(expression.key) {
				if len(expression.key) > 0 && expression.key[0] == "otel" {
					return 0, 0, false
				}
				if active != "" && len(seen) < 2 {
					return 0, 0, false
				}
				active = ""
				continue
			}
			name := expression.key[1]
			if seen[name] {
				return 0, 0, false
			}
			seen[name] = true
			active = name
			if first < 0 {
				first = expression.start
			}
			last = expression.end
		case unstable.KeyValue:
			if active == "" {
				continue
			}
			if len(expression.key) != 1 || !isCodexTelemetryKey(expression.key[0]) {
				return 0, 0, false
			}
			last = expression.end
		}
	}
	if len(seen) != 2 {
		return 0, 0, false
	}
	return first, last, true
}

func isCodexTelemetryTable(key []string) bool {
	return len(key) == 3 && key[0] == "otel" && key[2] == "otlp-http" &&
		(key[1] == "metrics_exporter" || key[1] == "exporter")
}

func isCodexTelemetryKey(key string) bool {
	return key == "endpoint" || key == "protocol" || key == "headers"
}

// hasOTELTOML reports competing root OTEL settings. Invalid TOML fails closed.
func hasOTELTOML(data []byte) bool {
	var decoded map[string]any
	if err := toml.Unmarshal(data, &decoded); err != nil {
		return true
	}
	_, ok := decoded["otel"]
	return ok
}

// tomlManagedRange accepts markers only when they are standalone TOML comment
// expressions. Marker text in a value, including multiline strings, is never
// a range delimiter.
func tomlManagedRange(data []byte) (int, int, error) {
	expressions, err := parseTOML(data)
	if err != nil {
		return 0, 0, err
	}
	begin, finish := -1, -1
	for _, expression := range expressions {
		if expression.kind != unstable.Comment {
			continue
		}
		switch expression.comment {
		case telemetryBegin:
			if begin >= 0 || finish >= 0 {
				return 0, 0, errors.New("multiple or nested Navigara telemetry markers")
			}
			begin = expression.start
		case telemetryEnd:
			if begin < 0 || finish >= 0 {
				return 0, 0, errors.New("unmatched Navigara telemetry end marker")
			}
			finish = expression.end
		default:
			if bytes.Contains([]byte(expression.comment), []byte("navigara telemetry")) {
				return 0, 0, errors.New("malformed Navigara telemetry marker")
			}
		}
	}
	if (begin >= 0) != (finish >= 0) {
		return 0, 0, errors.New("unmatched Navigara telemetry marker")
	}
	if begin < 0 {
		return len(data), len(data), nil
	}
	return begin, finish, nil
}
