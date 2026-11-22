package github

import "kern/form"

// The templates a write fills live in package form, which the checker
// (package lint) reads too; these are their names in this package.
type (
	Template      = form.Template
	TemplateField = form.TemplateField
)

// The writes a template serves, as its actions name them.
const (
	IssueCreate   = form.IssueCreate
	CommentCreate = form.CommentCreate
)

// LoadTemplate reads the template name from dir: <dir>/<name>.json.
func LoadTemplate(dir, name string) (Template, error) { return form.LoadTemplate(dir, name) }

// ParseTemplate reads a template's JSON, keeping its fields in their order.
func ParseTemplate(data []byte) (Template, error) { return form.ParseTemplate(data) }
