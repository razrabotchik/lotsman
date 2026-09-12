package catalog

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/razrabotchik/lotsman/internal/domain"
)

// inputSchema renders the grouped tool input schema from the IR's input
// model; the group vocabulary itself lives in domain (FR-22).

// descriptionByteBudgetPerParameter bounds one schema description. Parameter
// prose reaches the model's context exactly like a tool description does, so
// it gets the same treatment: sanitized and bounded (FR-18/19).
const descriptionByteBudgetPerParameter = 400

// inputSchema builds the tool's grouped JSON Schema 2020-12 input schema.
//
// additionalProperties:false at every level is the point of the exercise: an
// argument the model invents must be a validation error, not something
// silently dropped -- or worse, smuggled into the query string.
func inputSchema(input domain.InputModel) map[string]any {
	properties := make(map[string]any, len(domain.GroupOrder))
	var requiredGroups []string

	for _, name := range domain.GroupOrder {
		if name == domain.GroupBody {
			if group := bodySchema(input.Body); group != nil {
				properties[name] = group
				if input.Body.Required {
					requiredGroups = append(requiredGroups, name)
				}
			}
			continue
		}

		members := make(map[string]any)
		var required []string
		for i := range input.Parameters {
			p := &input.Parameters[i]
			if domain.GroupOf(p.In) != name {
				continue
			}
			members[p.Name] = parameterSchema(p)
			if p.Required {
				required = append(required, p.Name)
			}
		}
		sort.Strings(required)

		group := map[string]any{
			"type":                 "object",
			"properties":           members,
			"additionalProperties": false,
		}
		if len(required) > 0 {
			group["required"] = required
			// A group is required exactly when something inside it is: the
			// model cannot satisfy a required path parameter without sending
			// the path object.
			requiredGroups = append(requiredGroups, name)
		}
		properties[name] = group
	}

	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(requiredGroups) > 0 {
		schema["required"] = requiredGroups // already in groupOrder order
	}
	return schema
}

// bodySchema renders the request body as its own argument group. Unlike the
// parameter groups it is not an object of members but the body schema itself,
// so a body that is an array or a scalar is published as what it is.
//
// An operation with no body has no group at all: publishing an empty one
// would invite a model to send something the API has no field for.
func bodySchema(body *domain.BodySpec) map[string]any {
	if body == nil {
		return nil
	}
	out := sanitizeSchema(map[string]any(body.Schema))
	description := sanitizeDescription(body.Description)
	if description == "" {
		description = toString(out["description"])
	}
	if description != "" {
		out["description"] = budgetBytes(description, descriptionByteBudgetPerParameter)
	}
	return out
}

// parameterSchema renders one parameter as a schema property: the normalized
// schema from the IR plus the parameter's own description, both sanitized.
func parameterSchema(p *domain.Parameter) map[string]any {
	out := sanitizeSchema(map[string]any(p.Schema))

	// The parameter's description wins over the schema's: it describes this
	// use of the schema, which may be shared by several parameters.
	description := sanitizeDescription(p.Description)
	if description == "" {
		description = toString(out["description"])
	}
	if note := defaultNote(p.Schema); note != "" {
		description = strings.TrimSpace(description + " " + note)
	}
	if description != "" {
		out["description"] = budgetBytes(description, descriptionByteBudgetPerParameter)
	}

	if p.Deprecated {
		out["deprecated"] = true
	}
	return out
}

// defaultNote turns a schema default into prose.
//
// The default is deliberately NOT published as a `default` keyword: the MCP
// SDK (and any client using a JSON Schema library that applies defaults) fills
// missing arguments from it before the handler runs, which would put a query
// parameter on the wire that the model never asked for. FR-24 says defaults
// are never applied silently, so lotsman tells the model what the API does
// instead of letting a schema keyword decide for it.
func defaultNote(schema domain.Schema) string {
	value, ok := schema["default"]
	if !ok {
		return ""
	}
	rendered, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return "The API uses " + string(rendered) + " when this is omitted."
}

// sanitizeSchema copies a schema, cleaning every human-readable string in it.
// The schema comes from an untrusted document and is published verbatim into
// an LLM's context, so its prose is an injection surface exactly like a tool
// description (Constitution V).
func sanitizeSchema(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		switch key {
		case "description", "title":
			if text := sanitizeDescription(toString(value)); text != "" {
				out[key] = text
			}
		case "default":
			// Never republished; see defaultNote.
		case "items", "additionalProperties":
			if nested, ok := value.(map[string]any); ok {
				out[key] = sanitizeSchema(nested)
				continue
			}
			out[key] = value
		case "properties":
			nested, ok := value.(map[string]any)
			if !ok {
				out[key] = value
				continue
			}
			cleaned := make(map[string]any, len(nested))
			for name, property := range nested {
				if sub, ok := property.(map[string]any); ok {
					cleaned[name] = sanitizeSchema(sub)
					continue
				}
				cleaned[name] = property
			}
			out[key] = cleaned
		case "allOf", "oneOf", "anyOf":
			branches, ok := value.([]any)
			if !ok {
				out[key] = value
				continue
			}
			cleaned := make([]any, 0, len(branches))
			for _, branch := range branches {
				if sub, ok := branch.(map[string]any); ok {
					cleaned = append(cleaned, sanitizeSchema(sub))
					continue
				}
				cleaned = append(cleaned, branch)
			}
			out[key] = cleaned
		default:
			out[key] = value
		}
	}
	return out
}

func sanitizeDescription(s string) string {
	return budgetBytes(sanitizeText(s), descriptionByteBudgetPerParameter)
}

func toString(value any) string {
	s, _ := value.(string)
	return s
}
