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
		schema["required"] = requiredGroups // already in domain.GroupOrder order
	}
	// The tool's input schema is a standalone document, so the components its
	// groups point at travel with it. "#/$defs/..." resolves against this
	// root, which is why the bundle belongs here and not inside a group.
	if len(input.Defs) > 0 {
		defs := make(map[string]any, len(input.Defs))
		for name, def := range input.Defs {
			defs[name] = sanitizeSchema(map[string]any(def))
		}
		schema["$defs"] = defs
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
		switch {
		case key == "description" || key == "title":
			if text := sanitizeDescription(toString(value)); text != "" {
				out[key] = text
			}
		case key == "examples":
			// Illustrative, not a constraint -- so unlike `enum` and `const`
			// it can be cleaned without changing what the schema accepts, and
			// unlike them it is written to be read. It reaches a model's
			// context exactly as a description does, and until this line it
			// was the one piece of document prose that got there uncleaned.
			if cleaned := sanitizeData(value); cleaned != nil {
				out[key] = cleaned
			}
		case key == "default":
			// Never republished; see defaultNote.
		case domain.SchemaValuedKeywords[key]:
			if nested, ok := value.(map[string]any); ok {
				out[key] = sanitizeSchema(nested)
				continue
			}
			out[key] = value
		case domain.NamedSchemaKeywords[key]:
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
		case domain.SchemaListKeywords[key]:
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
			// Data the API defined, or a keyword this build does not know.
			// `enum` and `const` in particular are constraints: cleaning them
			// would change what the schema accepts, which is a worse failure
			// than the prose they might carry.
			out[key] = value
		}
	}
	return out
}

// sanitizeData cleans the strings inside an illustrative value, at any depth,
// and leaves its shape alone.
func sanitizeData(value any) any {
	switch typed := value.(type) {
	case string:
		return sanitizeDescription(typed)
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = sanitizeData(item)
		}
		return out
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, sanitizeData(item))
		}
		return out
	default:
		return value
	}
}

func sanitizeDescription(s string) string {
	return budgetBytes(sanitizeText(s), descriptionByteBudgetPerParameter)
}

func toString(value any) string {
	s, _ := value.(string)
	return s
}
