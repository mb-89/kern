// Package filled is the one definition of how a kern component asks an
// application's template component whether it accepts a filled template: the
// request port template-accept, its shapes template.accept and
// template.accepted, and their Go types. A component that writes only what a
// template filled, the GitHub component's issue-create and issue-comment
// among them, declares the port with Port and asks it with
// index.RequestPort.Within; the application's wiring binds it to its
// template component's verb accept. kern imports no application: the filled
// template is the application's type, which only its template component
// makes and checks, and which kern passes on as it stands (README.md
// "Filled templates").
package filled

import "kern/index"

// The request port's local name and the named JSON shapes of its request and
// answer, as the template component's verb accept takes and answers them.
const (
	PortName      = "template-accept"
	AcceptShape   = "template.accept"
	AcceptedShape = "template.accepted"
)

// Accept is what the port asks: the target that was handed the value, the
// template it requires ("" takes any) or the path of the file it writes, and
// the value itself, as the caller passed it on.
type Accept struct {
	Target   string `json:"target"`
	Template string `json:"template,omitempty"`
	Path     string `json:"path,omitempty"`
	Filled   any    `json:"filled"`
}

// Accepted is what accept answers: the filled template's template, version,
// title and document, which the target writes as they stand; Free where the
// path is one no template claims, so the target takes the write as it is.
type Accepted struct {
	Free     bool    `json:"free"`
	Template string  `json:"template,omitempty"`
	Version  string  `json:"version,omitempty"`
	Title    *string `json:"title,omitempty"`
	Document string  `json:"document,omitempty"`
}

// Port declares the request port template-accept with help, optional, so a
// wiring without a template component starts and every ask on it is refused,
// naming the wire that binds it.
func Port(help string) *index.RequestPort[any, any] {
	return index.JSONRequest(PortName, help, AcceptShape, AcceptedShape).Optional()
}
