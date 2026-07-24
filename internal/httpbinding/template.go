package httpbinding

import (
	"fmt"
	"regexp"
	"strings"
)

const maxTemplateDepth = 64

var formatArgumentPattern = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_-]*)\}`)

func RenderTemplate(template interface{}, args map[string]interface{}) (interface{}, error) {
	value, present, err := renderTemplate(template, args, 0)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("root template cannot be omitted")
	}
	return value, nil
}

func renderTemplate(template interface{}, args map[string]interface{}, depth int) (interface{}, bool, error) {
	if depth > maxTemplateDepth {
		return nil, false, fmt.Errorf("template nesting exceeds %d levels", maxTemplateDepth)
	}

	switch item := template.(type) {
	case map[string]interface{}:
		if argument, marker := item["$arg"]; marker {
			name, ok := argument.(string)
			if !ok || strings.TrimSpace(name) == "" {
				return nil, false, fmt.Errorf("$arg must name an input")
			}
			if value, exists := args[name]; exists {
				return cloneJSONValue(value, depth+1)
			}
			if fallback, exists := item["$default"]; exists {
				return renderTemplate(fallback, args, depth+1)
			}
			if omit, _ := item["$omit_if_missing"].(bool); omit {
				return nil, false, nil
			}
			return nil, false, fmt.Errorf("template argument %q is missing", name)
		}
		if format, marker := item["$format"]; marker {
			text, ok := format.(string)
			if !ok {
				return nil, false, fmt.Errorf("$format must be a string")
			}
			rendered := ""
			last := 0
			for _, location := range formatArgumentPattern.FindAllStringSubmatchIndex(text, -1) {
				rendered += text[last:location[0]]
				name := text[location[2]:location[3]]
				value, exists := args[name]
				if !exists {
					return nil, false, fmt.Errorf("format argument %q is missing", name)
				}
				switch value.(type) {
				case map[string]interface{}, []interface{}:
					return nil, false, fmt.Errorf("format argument %q must be a scalar", name)
				}
				rendered += fmt.Sprint(value)
				last = location[1]
			}
			rendered += text[last:]
			return rendered, true, nil
		}

		result := make(map[string]interface{}, len(item))
		for key, child := range item {
			value, present, err := renderTemplate(child, args, depth+1)
			if err != nil {
				return nil, false, fmt.Errorf("%s: %w", key, err)
			}
			if present {
				result[key] = value
			}
		}
		return result, true, nil
	case []interface{}:
		result := make([]interface{}, 0, len(item))
		for index, child := range item {
			value, present, err := renderTemplate(child, args, depth+1)
			if err != nil {
				return nil, false, fmt.Errorf("[%d]: %w", index, err)
			}
			if present {
				result = append(result, value)
			}
		}
		return result, true, nil
	default:
		return item, true, nil
	}
}

func cloneJSONValue(value interface{}, depth int) (interface{}, bool, error) {
	if depth > maxTemplateDepth {
		return nil, false, fmt.Errorf("argument nesting exceeds %d levels", maxTemplateDepth)
	}
	switch item := value.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(item))
		for key, child := range item {
			cloned, _, err := cloneJSONValue(child, depth+1)
			if err != nil {
				return nil, false, err
			}
			result[key] = cloned
		}
		return result, true, nil
	case []interface{}:
		result := make([]interface{}, len(item))
		for index, child := range item {
			cloned, _, err := cloneJSONValue(child, depth+1)
			if err != nil {
				return nil, false, err
			}
			result[index] = cloned
		}
		return result, true, nil
	default:
		return value, true, nil
	}
}
