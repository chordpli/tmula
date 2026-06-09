package load

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// ExtractVariables reads JSON response fields into template variables. Paths are
// dot-separated object keys with numeric array indexes, e.g. "items.0.id" or
// "$.cart.id".
func ExtractVariables(body []byte, spec map[string]string) (map[string]string, error) {
	if len(spec) == 0 {
		return nil, nil
	}
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("load: extract response json: %w", err)
	}
	out := make(map[string]string, len(spec))
	for name, path := range spec {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("load: extract variable name is empty")
		}
		value, err := lookupJSONPath(root, path)
		if err != nil {
			return nil, fmt.Errorf("load: extract %s from %q: %w", name, path, err)
		}
		text, err := extractedString(value)
		if err != nil {
			return nil, fmt.Errorf("load: extract %s from %q: %w", name, path, err)
		}
		out[name] = text
	}
	return out, nil
}

func lookupJSONPath(root any, rawPath string) (any, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" || path == "$" {
		return root, nil
	}
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return root, nil
	}

	cur := root
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			return nil, fmt.Errorf("empty path segment")
		}
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[part]
			if !ok {
				return nil, fmt.Errorf("missing key %q", part)
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("array index %q is not numeric", part)
			}
			if i < 0 || i >= len(node) {
				return nil, fmt.Errorf("array index %d out of range", i)
			}
			cur = node[i]
		default:
			return nil, fmt.Errorf("cannot descend into %T at %q", cur, part)
		}
	}
	return cur, nil
}

func extractedString(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
}
