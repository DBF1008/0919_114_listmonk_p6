package models

import (
	"html/template"
	"testing"

	null "gopkg.in/volatiletech/null.v6"
)

func newTestCampaign() *Campaign {
	return &Campaign{
		Subject:      "Test subject",
		FromEmail:    "test@example.com",
		Body:         "<p>hello {{ .Subscriber.Email }}</p>",
		ContentType:  CampaignContentTypeHTML,
		TemplateBody: `<html><body>{{ template "content" . }}</body></html>`,
		TemplateID:   null.IntFrom(1),
	}
}

func TestCompileTemplate(t *testing.T) {
	c := newTestCampaign()
	if err := c.CompileTemplate(template.FuncMap{}); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}
	if c.Tpl == nil {
		t.Fatal("expected Tpl to be compiled")
	}
	if c.SubjectTpl != nil {
		t.Fatal("expected SubjectTpl to be nil for a non-templated subject")
	}
	if c.HeaderTpls != nil {
		t.Fatal("expected HeaderTpls to be nil for non-templated headers")
	}
}

func TestCompileTemplateHeaders(t *testing.T) {
	c := newTestCampaign()
	c.Headers = Headers{
		{"X-Custom": "static-value"},
		{"X-Templated": "hello {{ .Subscriber.Name }}"},
	}
	if err := c.CompileTemplate(template.FuncMap{}); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}
	if c.HeaderTpls == nil {
		t.Fatal("expected HeaderTpls to be compiled")
	}
	if len(c.HeaderTpls) != 2 {
		t.Fatalf("expected 2 header sets, got %d", len(c.HeaderTpls))
	}
	if len(c.HeaderTpls[0]) != 0 {
		t.Fatal("expected no compiled templates in the static header set")
	}
	if c.HeaderTpls[1]["X-Templated"] == nil {
		t.Fatal("expected X-Templated header to be compiled")
	}
}

func TestCompileTemplateSubject(t *testing.T) {
	c := newTestCampaign()
	c.Subject = "Hi {{ .Subscriber.Name }}"
	if err := c.CompileTemplate(template.FuncMap{}); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}
	if c.SubjectTpl == nil {
		t.Fatal("expected SubjectTpl to be compiled")
	}
}

func TestTplHash(t *testing.T) {
	base := newTestCampaign()
	hash := base.TplHash()

	// Hash must be deterministic.
	if base.TplHash() != hash {
		t.Fatal("expected hash to be deterministic")
	}

	// Map iteration order must not affect the hash.
	c := newTestCampaign()
	c.Headers = Headers{
		{"B": "2", "A": "1"},
		{"C": "3"},
	}
	c2 := newTestCampaign()
	c2.Headers = Headers{
		{"A": "1", "B": "2"},
		{"C": "3"},
	}
	if c.TplHash() != c2.TplHash() {
		t.Fatal("expected header map order to not affect the hash")
	}

	// Every template-affecting field must invalidate the hash.
	cases := map[string]func(c *Campaign){
		"template body": func(c *Campaign) { c.TemplateBody = "changed" },
		"body":          func(c *Campaign) { c.Body = "changed" },
		"subject":       func(c *Campaign) { c.Subject = "changed" },
		"content type":  func(c *Campaign) { c.ContentType = CampaignContentTypePlain },
		"altbody":       func(c *Campaign) { c.AltBody = null.StringFrom("changed") },
		"headers":       func(c *Campaign) { c.Headers = Headers{{"X-A": "1"}} },
	}
	for name, mutate := range cases {
		c := newTestCampaign()
		mutate(c)
		if c.TplHash() == hash {
			t.Fatalf("expected hash to change when %s changes", name)
		}
	}
}
