// Generate the checked-in OpenAPI document from the exported wire types.
package main

import (
	"encoding/json"
	"github.com/UwUOcha/dairy-303-public/pkg/provider"
	"os"
	"reflect"
	"strings"
)

type object = map[string]any

var schemas = object{}

func schema(t reflect.Type) object {
	if t.Kind() == reflect.Struct {
		name := t.Name()
		if _, ok := schemas[name]; !ok {
			schemas[name] = object{}
			properties := object{}
			required := []string{}
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				parts := strings.Split(f.Tag.Get("json"), ",")
				key := parts[0]
				if key == "" || key == "-" {
					continue
				}
				properties[key] = schema(f.Type)
				if len(parts) == 1 {
					required = append(required, key)
				}
			}
			schemas[name] = object{"type": "object", "properties": properties, "required": required}
		}
		return object{"$ref": "#/components/schemas/" + name}
	}
	switch t.Kind() {
	case reflect.String:
		return object{"type": "string"}
	case reflect.Bool:
		return object{"type": "boolean"}
	case reflect.Int, reflect.Int64:
		return object{"type": "integer"}
	case reflect.Slice:
		return object{"type": "array", "items": schema(t.Elem())}
	case reflect.Map:
		return object{"type": "object", "additionalProperties": schema(t.Elem())}
	}
	panic(t.String())
}
func response(t any) object {
	return object{"description": "Successful response", "content": object{"application/json": object{"schema": schema(reflect.TypeOf(t))}}}
}
func main() {
	paths := object{}
	for _, endpoint := range []struct {
		path, operation string
		value           any
	}{{"/v1/info", "getInfo", provider.Info{}}, {"/v1/catalog", "getCatalog", provider.Catalog{}}, {"/v1/schedule", "getSchedule", provider.Snapshot{}}, {"/v1/teachers", "getTeachers", provider.Directory{}}} {
		responses := object{"200": response(endpoint.value)}
		for _, code := range []string{"400", "401", "404", "405", "429", "501", "502", "503"} {
			responses[code] = response(provider.Error{})
		}
		operation := object{"operationId": endpoint.operation, "responses": responses}
		if endpoint.path == "/v1/schedule" {
			parameters := []any{}
			for _, name := range []string{"group", "from", "to"} {
				s := object{"type": "string"}
				if name != "group" {
					s["format"] = "date"
				}
				parameters = append(parameters, object{"name": name, "in": "query", "required": true, "schema": s})
			}
			operation["parameters"] = parameters
			operation["description"] = "Complete inclusive range, at most 31 dates. Core v1 requests calendar months. Never represent failed or unpublished data as a published empty snapshot."
		}
		paths[endpoint.path] = object{"get": operation}
	}
	paths["/health"] = object{"get": object{"operationId": "health", "security": []any{}, "responses": object{"200": object{"description": "Process is alive; does not promise source availability"}}}}
	doc := object{"openapi": "3.1.1", "info": object{"title": "University schedule adapter API", "version": "1.0.0", "description": "See docs/ADAPTERS.md for identity, snapshot, optional capabilities, and compatibility semantics."}, "paths": paths, "security": []any{object{"serviceToken": []string{}}}, "components": object{"schemas": schemas, "securitySchemes": object{"serviceToken": object{"type": "http", "scheme": "bearer"}}}}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if e := encoder.Encode(doc); e != nil {
		panic(e)
	}
}
