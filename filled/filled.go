// Package filled is the gate every templated target in kern asks: the request
// port template-accept, whose shapes template.accept and template.accepted an
// application's template component answers with its verb accept (SE#1683, the
// person's decision of 22 Nov, typed templates: a filled template is a type
// only the template component constructs, and every templated target accepts
// only that type). kern imports no application's package, so a target in kern
// holds no filled template's type: it hands the value it was given to the gate
// as it stands and writes the title and document the gate answers.
//
// One definition serves each target, the files module's template-only paths
// (components/files) and the GitHub door's writing tools (components/github),
// so an application's wiring binds every one of them to the same verb:
//
//	[wires]
//	"files.template-accept" = "template.accept"
package filled

import (
	"context"
	"fmt"

	"kern/index"
)

// The port and its shapes, as an application's template component declares
// its verb accept.
const (
	// Port is the local name of the request port a templated target asks.
	Port = "template-accept"
	// RequestShape is the named JSON shape of the gate's request.
	RequestShape = "template.accept"
	// AnswerShape is the named JSON shape of the gate's answer.
	AnswerShape = "template.accepted"
)

// Request is what a target asks the gate: its name, for the refusal; the
// template it requires, or the path of the file it writes, whose claiming
// template is then required; and Filled, the value it was handed, as it
// stands: the JSON the template component's fill answered, or free text the
// gate refuses wherever a template is required.
type Request struct {
	Target   string `json:"target"`
	Template string `json:"template,omitempty"`
	Path     string `json:"path,omitempty"`
	Filled   any    `json:"filled"`
}

// Accepted is the gate's answer: Free where the path is one no template
// claims, so the target takes the write as it was given; otherwise the
// filled template's template, version, title and document, which the target
// writes as they stand.
type Accepted struct {
	Free     bool    `json:"free"`
	Template string  `json:"template,omitempty"`
	Version  string  `json:"version,omitempty"`
	Title    *string `json:"title,omitempty"`
	Document string  `json:"document,omitempty"`
}

// NewPort declares a target's request port template-accept, with its help
// line; the application's wiring binds it to its template component's verb
// accept.
func NewPort(help string) *index.RequestPort[any, any] {
	return index.JSONRequest(Port, help, RequestShape, AnswerShape)
}

// Answer reads the gate's answer as a continuation is handed it.
func Answer(a any) (Accepted, error) {
	var out Accepted
	if err := index.Recode(a, &out); err != nil {
		return Accepted{}, fmt.Errorf("the gate %s answered no %s: %w", Port, AnswerShape, err)
	}
	return out, nil
}

// FreeVerb is a verb of the gate's shapes that answers every path free: a
// target whose wiring binds its port to nothing binds it to this verb of its
// own (index.RequestPort.Self), so an application without templates writes
// as it did.
func FreeVerb(name, help string) index.Part {
	return index.JSONVerb(name, help, RequestShape, AnswerShape, func(_ context.Context, _ Request) (Accepted, error) {
		return Accepted{Free: true}, nil
	})
}
