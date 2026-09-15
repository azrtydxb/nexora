// Package apispec embeds the management OpenAPI document and indexes its operations by operationId.
package apispec

import (
	"context"
	_ "embed"
	"sort"
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
)

//go:embed openapi.yaml
var document []byte

// Param is one path, query or header parameter of an operation.
type Param struct {
	Name, In string
	Required bool
	Schema   *openapi3.SchemaRef
}

// Operation is one OpenAPI operation: its upper-case method, its path template (without the
// /api/v1 server prefix), its merged path-level and operation-level parameters, and its JSON
// request body schema (nil without a body).
type Operation struct {
	ID, Method, Path string
	Params           []Param
	Body             *openapi3.SchemaRef
}

var (
	loadOnce sync.Once
	spec     *openapi3.T
	specErr  error
	ops      map[string]Operation
)

func load() {
	doc, err := openapi3.NewLoader().LoadFromData(document)
	if err == nil {
		err = doc.Validate(context.Background())
	}
	if err != nil {
		specErr = err
		return
	}
	spec, ops = doc, index(doc)
}

// Spec returns the embedded document, loaded and validated once.
func Spec() (*openapi3.T, error) {
	loadOnce.Do(load)
	return spec, specErr
}

// Operations returns every operation by operationId. It panics when the compiled-in document does
// not load, which a build-time test rules out.
func Operations() map[string]Operation {
	if _, err := Spec(); err != nil {
		panic("apispec: embedded openapi.yaml: " + err.Error())
	}
	return ops
}

func index(doc *openapi3.T) map[string]Operation {
	out := map[string]Operation{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			o := Operation{ID: op.OperationID, Method: strings.ToUpper(method), Path: path}
			byKey := map[string]Param{}
			for _, list := range []openapi3.Parameters{item.Parameters, op.Parameters} {
				for _, ref := range list {
					if p := ref.Value; p != nil {
						byKey[p.In+"\x00"+p.Name] = Param{Name: p.Name, In: p.In, Required: p.Required, Schema: p.Schema}
					}
				}
			}
			for _, p := range byKey {
				o.Params = append(o.Params, p)
			}
			sort.Slice(o.Params, func(i, j int) bool {
				if o.Params[i].In != o.Params[j].In {
					return o.Params[i].In < o.Params[j].In
				}
				return o.Params[i].Name < o.Params[j].Name
			})
			if rb := op.RequestBody; rb != nil && rb.Value != nil {
				if mt := rb.Value.Content.Get("application/json"); mt != nil {
					o.Body = mt.Schema
				}
			}
			out[op.OperationID] = o
		}
	}
	return out
}
