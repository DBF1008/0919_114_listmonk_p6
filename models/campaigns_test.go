package models

import (
	"bytes"
	"html/template"
	"strings"
	"testing"

	null "gopkg.in/volatiletech/null.v6"
)

func testFuncs() template.FuncMap {
	return template.FuncMap{
		"TrackLink": func(url string, msg any) string { return "http://track.example/" + url },
	}
}

func newTestCampaign() Campaign {
	return Campaign{
		CampaignMeta: CampaignMeta{},
		Subject:      "Hello {{ index . \"name\" }}",
		ContentType:  CampaignContentTypeHTML,
		TemplateBody: `<html>{{ template "content" . }}</html>`,
		Body:         `<p>Hi {{ index . "name" }}</p>`,
		AltBody:      null.StringFrom("alt {{ index . \"name\" }}"),
		TemplateID:   null.IntFrom(1),
		Headers: Headers{
			{"X-Static": "static-value"},
		},
	}
}

func executeTpl(t *testing.T, c *Campaign, data any) string {
	t.Helper()

	var b bytes.Buffer
	if err := c.Tpl.ExecuteTemplate(&b, BaseTpl, data); err != nil {
		t.Fatalf("error executing template: %v", err)
	}

	return b.String()
}

func TestCompileCampaignTpl_Basic(t *testing.T) {
	c := newTestCampaign()

	tpl, err := CompileCampaignTpl(&c, testFuncs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.SetCompiledTpl(tpl)

	if c.Tpl == nil || c.SubjectTpl == nil || c.AltBodyTpl == nil {
		t.Fatal("expected subject, body and alt body templates to be compiled")
	}

	out := executeTpl(t, &c, map[string]any{"name": "bob"})
	if !strings.Contains(out, "<p>Hi bob</p>") {
		t.Fatalf("unexpected rendered body: %s", out)
	}
	if !strings.HasPrefix(out, "<html>") {
		t.Fatalf("base template wrapper missing: %s", out)
	}

	var subj bytes.Buffer
	if err := c.SubjectTpl.ExecuteTemplate(&subj, ContentTpl, map[string]any{"name": "bob"}); err != nil {
		t.Fatalf("error executing subject: %v", err)
	}
	if subj.String() != "Hello bob" {
		t.Fatalf("unexpected subject: %s", subj.String())
	}

	var alt bytes.Buffer
	if err := c.AltBodyTpl.ExecuteTemplate(&alt, ContentTpl, map[string]any{"name": "bob"}); err != nil {
		t.Fatalf("error executing alt body: %v", err)
	}
	if alt.String() != "alt bob" {
		t.Fatalf("unexpected alt body: %s", alt.String())
	}
}

func TestCompileCampaignTpl_Markdown(t *testing.T) {
	c := newTestCampaign()
	c.ContentType = CampaignContentTypeMarkdown
	c.Body = "# Title"

	tpl, err := CompileCampaignTpl(&c, testFuncs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.SetCompiledTpl(tpl)

	out := executeTpl(t, &c, nil)
	if !strings.Contains(out, "<h1 id=\"title\">Title</h1>") {
		t.Fatalf("expected markdown->HTML conversion, got: %s", out)
	}
}

func TestCompileCampaignTpl_VisualFallback(t *testing.T) {
	c := newTestCampaign()
	c.ContentType = CampaignContentTypeVisual
	c.TemplateBody = ""

	tpl, err := CompileCampaignTpl(&c, testFuncs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.SetCompiledTpl(tpl)

	out := executeTpl(t, &c, map[string]any{"name": "bob"})
	if strings.Contains(out, "<html>") {
		t.Fatalf("visual campaigns should use the fallback base template, got: %s", out)
	}
	if !strings.Contains(out, "<p>Hi bob</p>") {
		t.Fatalf("unexpected body: %s", out)
	}
}

func TestCompileCampaignTpl_StaticHeadersNotCompiled(t *testing.T) {
	c := newTestCampaign()

	tpl, err := CompileCampaignTpl(&c, testFuncs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if tpl.HeaderTpls != nil {
		t.Fatal("expected no header templates for static headers")
	}
}

func TestCompileCampaignTpl_TemplatedHeadersCompiledOnce(t *testing.T) {
	c := newTestCampaign()
	c.Headers = Headers{
		{"X-Static": "plain"},
		{"X-Dynamic": "hello {{ \"world\" }}", "X-Also": `{{ print "abc" }}`},
	}

	tpl, err := CompileCampaignTpl(&c, testFuncs())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(tpl.HeaderTpls) != len(c.Headers) {
		t.Fatalf("expected %d header sets, got %d", len(c.Headers), len(tpl.HeaderTpls))
	}
	if tpl.HeaderTpls[0] != nil {
		t.Fatal("static header set should not be compiled")
	}
	if tpl.HeaderTpls[1]["X-Dynamic"] == nil || tpl.HeaderTpls[1]["X-Also"] == nil {
		t.Fatal("expected templated headers to be compiled")
	}

	var b bytes.Buffer
	if err := tpl.HeaderTpls[1]["X-Dynamic"].ExecuteTemplate(&b, ContentTpl, nil); err != nil {
		t.Fatalf("error executing header template: %v", err)
	}
	if b.String() != "hello world" {
		t.Fatalf("unexpected header render: %s", b.String())
	}
}

func TestCompileCampaignTpl_InvalidTemplate(t *testing.T) {
	c := newTestCampaign()
	c.Body = `{{ .Broken`

	if _, err := CompileCampaignTpl(&c, testFuncs()); err == nil {
		t.Fatal("expected a compilation error")
	}
}

func TestCompileTemplate_Wrapper(t *testing.T) {
	c := newTestCampaign()
	if err := c.CompileTemplate(testFuncs()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Tpl == nil {
		t.Fatal("expected Tpl to be set on the campaign")
	}
}

func TestTplFingerprint_Stable(t *testing.T) {
	c1 := newTestCampaign()
	c2 := newTestCampaign()

	b1, h1 := c1.TplFingerprint()
	b2, h2 := c2.TplFingerprint()
	if b1 != b2 || h1 != h2 {
		t.Fatal("identical campaigns must have identical fingerprints")
	}
}

func TestTplFingerprint_ContentChanges(t *testing.T) {
	orig := newTestCampaign()
	_, origContent := orig.TplFingerprint()
	cases := []struct {
		name string
		mut  func(c *Campaign)
	}{
		{"subject", func(c *Campaign) { c.Subject = "different" }},
		{"body", func(c *Campaign) { c.Body = "different body" }},
		{"altbody", func(c *Campaign) { c.AltBody = null.StringFrom("different alt") }},
		{"content-type", func(c *Campaign) { c.ContentType = CampaignContentTypePlain }},
		{"header-value", func(c *Campaign) { c.Headers[0]["X-Static"] = "changed" }},
		{"header-name", func(c *Campaign) {
			c.Headers = Headers{{"X-Other": "static-value"}}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCampaign()
			tc.mut(&c)
			_, h := c.TplFingerprint()
			if h == origContent {
				t.Fatalf("content hash must change when %s changes", tc.name)
			}
		})
	}
}

func TestTplFingerprint_BaseChanges(t *testing.T) {
	c := newTestCampaign()
	b1, _ := c.TplFingerprint()

	c.TemplateBody = `<body>{{ template "content" . }}</body>`
	b2, _ := c.TplFingerprint()
	if b1 == b2 {
		t.Fatal("base hash must change when the base template changes")
	}
}

func TestTplFingerprint_HeaderOrderIndependent(t *testing.T) {
	c1 := newTestCampaign()
	c1.Headers = Headers{{"X-A": "1", "X-B": "2"}}

	// Run several times to cover randomized map iteration order.
	for i := 0; i < 20; i++ {
		c2 := newTestCampaign()
		c2.Headers = Headers{{"X-B": "2", "X-A": "1"}}

		b1, h1 := c1.TplFingerprint()
		b2, h2 := c2.TplFingerprint()
		if b1 != b2 || h1 != h2 {
			t.Fatal("fingerprint must be independent of header map order")
		}
	}
}
