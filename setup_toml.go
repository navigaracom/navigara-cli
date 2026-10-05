package main

import (
	"strconv"
	"strings"
)

// hasOTELTOML recognizes only competing root OTEL keys/tables. It is a narrow lexer,
// not a TOML parser: strings, comments, arrays, and inline tables are skipped safely.
func hasOTELTOML(data []byte) bool {
	state := ""
	depth := 0
	beforeTable := true
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if state == "" && depth == 0 && line != "" && !strings.HasPrefix(line, "#") {
			table := strings.HasPrefix(line, "[")
			key := tomlRootKey(line)
			if key == "otel" && (table || beforeTable && strings.Contains(line, "=")) {
				return true
			}
			if table {
				beforeTable = false
			}
		}
		state, depth = scanTOML(line, state, depth)
	}
	return state != "" || depth != 0
}

func scanTOML(line, state string, depth int) (string, int) {
	for i := 0; i < len(line); {
		if state != "" {
			if len(state) == 3 && strings.HasPrefix(line[i:], state) {
				i += 3
				for extra := 0; extra < 2 && i < len(line) && line[i] == state[0]; extra++ {
					i++
				}
				state = ""
			} else if len(state) == 1 && line[i] == state[0] {
				state, i = "", i+1
			} else if state[0] == '"' && line[i] == '\\' {
				i += 2
			} else {
				i++
			}
			continue
		}
		if line[i] == '#' {
			return state, depth
		}
		if line[i] == '"' || line[i] == '\'' {
			state, i = line[i:i+1], i+1
			if i+1 < len(line) && line[i] == state[0] && line[i+1] == state[0] {
				state, i = line[i-1:i+2], i+2
			}
			continue
		}
		switch line[i] {
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		}
		i++
	}
	return state, depth
}

func tomlRootKey(line string) string {
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "[") {
		line = strings.TrimSpace(strings.TrimLeft(line, "["))
	}
	if strings.HasPrefix(line, "\"") {
		for i := 1; i < len(line); i++ {
			if line[i] == '\\' {
				i++
				continue
			}
			if line[i] == '"' {
				key, err := strconv.Unquote(line[:i+1])
				if err == nil {
					return key
				}
				return ""
			}
		}
		return ""
	}
	if strings.HasPrefix(line, "'") {
		if i := strings.Index(line[1:], "'"); i >= 0 {
			return line[1 : i+1]
		}
		return ""
	}
	for i, r := range line {
		if r == '.' || r == ']' || r == '=' || r == ' ' || r == '\t' {
			return line[:i]
		}
	}
	return line
}
