package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"html/template"
	"io"
	"sort"
	"strings"
	txttpl "text/template"

	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/types"
	"github.com/lib/pq"
	null "gopkg.in/volatiletech/null.v6"
)

const (
	CampaignStatusDraft         = "draft"
	CampaignStatusScheduled     = "scheduled"
	CampaignStatusRunning       = "running"
	CampaignStatusPaused        = "paused"
	CampaignStatusFinished      = "finished"
	CampaignStatusCancelled     = "cancelled"
	CampaignTypeRegular         = "regular"
	CampaignTypeOptin           = "optin"
	CampaignContentTypeRichtext = "richtext"
	CampaignContentTypeHTML     = "html"
	CampaignContentTypeMarkdown = "markdown"
	CampaignContentTypePlain    = "plain"
	CampaignContentTypeVisual   = "visual"
)

// Campaigns represents a slice of Campaigns.
type Campaigns []Campaign

// Campaign represents an e-mail campaign.
type Campaign struct {
	Base
	CampaignMeta

	UUID              string          `db:"uuid" json:"uuid"`
	Type              string          `db:"type" json:"type"`
	Name              string          `db:"name" json:"name"`
	Subject           string          `db:"subject" json:"subject"`
	FromEmail         string          `db:"from_email" json:"from_email"`
	Body              string          `db:"body" json:"body"`
	BodySource        null.String     `db:"body_source" json:"body_source"`
	AltBody           null.String     `db:"altbody" json:"altbody"`
	SendAt            null.Time       `db:"send_at" json:"send_at"`
	Status            string          `db:"status" json:"status"`
	ContentType       string          `db:"content_type" json:"content_type"`
	Tags              pq.StringArray  `db:"tags" json:"tags"`
	Headers           Headers         `db:"headers" json:"headers"`
	Attribs           JSON            `db:"attribs" json:"attribs"`
	TemplateID        null.Int        `db:"template_id" json:"template_id"`
	Messenger         string          `db:"messenger" json:"messenger"`
	Archive           bool            `db:"archive" json:"archive"`
	ArchiveSlug       null.String     `db:"archive_slug" json:"archive_slug"`
	ArchiveTemplateID null.Int        `db:"archive_template_id" json:"archive_template_id"`
	ArchiveMeta       json.RawMessage `db:"archive_meta" json:"archive_meta"`

	// TemplateBody is joined in from templates by the next-campaigns query.
	TemplateBody        string             `db:"template_body" json:"-"`
	ArchiveTemplateBody string             `db:"archive_template_body" json:"-"`
	Tpl                 *template.Template `json:"-"`
	SubjectTpl          *txttpl.Template   `json:"-"`
	AltBodyTpl          *template.Template `json:"-"`

	// HeaderTpls is holds optionally {{ templated }} campaign headers.
	HeaderTpls []map[string]*txttpl.Template `json:"-"`

	// List of media (attachment) IDs obtained from the next-campaign query
	// while sending a campaign.
	MediaIDs pq.Int64Array `json:"-" db:"media_id"`

	// Fetched bodies of the attachments.
	Attachments []Attachment `json:"-" db:"-"`

	// Pseudofield for getting the total number of subscribers
	// in searches and queries.
	Total int `db:"total" json:"-"`
}

// CampaignMeta contains fields tracking a campaign's progress.
type CampaignMeta struct {
	CampaignID int `db:"campaign_id" json:"-"`
	Views      int `db:"views" json:"views"`
	Clicks     int `db:"clicks" json:"clicks"`
	Bounces    int `db:"bounces" json:"bounces"`

	// This is a list of {list_id, name} pairs unlike Subscriber.Lists[]
	// because lists can be deleted after a campaign is finished, resulting
	// in null lists data to be returned. For that reason, campaign_lists maintains
	// campaign-list associations with a historical record of id + name that persist
	// even after a list is deleted.
	Lists types.JSONText `db:"lists" json:"lists"`
	Media types.JSONText `db:"media" json:"media"`

	StartedAt null.Time `db:"started_at" json:"started_at"`
	ToSend    int       `db:"to_send" json:"to_send"`
	Sent      int       `db:"sent" json:"sent"`
}

// GetIDs returns the list of campaign IDs.
func (camps Campaigns) GetIDs() []int {
	IDs := make([]int, len(camps))
	for i, c := range camps {
		IDs[i] = c.ID
	}

	return IDs
}

// LoadStats lazy loads campaign stats onto a list of campaigns.
func (camps Campaigns) LoadStats(stmt *sqlx.Stmt) error {
	var meta []CampaignMeta
	if err := stmt.Select(&meta, pq.Array(camps.GetIDs())); err != nil {
		return err
	}

	if len(camps) != len(meta) {
		return errors.New("campaign stats count does not match")
	}

	for i, c := range meta {
		if c.CampaignID == camps[i].ID {
			camps[i].Lists = c.Lists
			camps[i].Views = c.Views
			camps[i].Clicks = c.Clicks
			camps[i].Bounces = c.Bounces
			camps[i].Media = c.Media
		}
	}

	return nil
}

// CompiledTpl holds the compiled template artifacts of a campaign. A single
// CompiledTpl can be shared across campaigns (and across thousands of messages)
// as html/template and text/template are safe for concurrent execution. The
// manager caches it keyed by TemplateID and the content fingerprints returned
// by Campaign.TplFingerprint(), recompiling only when content changes.
type CompiledTpl struct {
	Tpl        *template.Template
	SubjectTpl *txttpl.Template
	AltBodyTpl *template.Template

	// HeaderTpls holds the compiled text/template for every header value that
	// contains a template expression. Header detection and compilation is done
	// once here instead of on every render. Nil entries indicate static headers.
	HeaderTpls []map[string]*txttpl.Template
}

// SetCompiledTpl assigns a cached CompiledTpl's artifacts to the campaign so
// that the existing per-campaign fields used during rendering are populated
// without a recompile.
func (c *Campaign) SetCompiledTpl(t *CompiledTpl) {
	c.Tpl = t.Tpl
	c.SubjectTpl = t.SubjectTpl
	c.AltBodyTpl = t.AltBodyTpl
	c.HeaderTpls = t.HeaderTpls
}

// CompileTemplate compiles a campaign body template into its base template and
// assigns the resultant artifacts to the campaign. Prefer the manager's cached
// compilation path; this wrapper exists for one-off compiles (previews, public
// pages) where no cache is available.
func (c *Campaign) CompileTemplate(f template.FuncMap) error {
	t, err := CompileCampaignTpl(c, f)
	if err != nil {
		return err
	}

	c.SetCompiledTpl(t)

	return nil
}

// CompileCampaignTpl compiles the subject, base + body, alt body and any
// templated headers of a campaign into a reusable CompiledTpl.
func CompileCampaignTpl(c *Campaign, f template.FuncMap) (*CompiledTpl, error) {
	out := &CompiledTpl{}

	// If the subject line has a template string, compile it.
	if hasTplExpr(c.Subject) {
		subj := c.Subject
		for _, r := range regTplFuncs {
			subj = r.regExp.ReplaceAllString(subj, r.replace)
		}

		var txtFuncs map[string]any = f
		subjTpl, err := txttpl.New(ContentTpl).Funcs(txtFuncs).Parse(subj)
		if err != nil {
			return nil, fmt.Errorf("error compiling subject: %v", err)
		}
		out.SubjectTpl = subjTpl
	}

	// Compile the base template.
	body := c.TemplateBody

	if body == "" || c.ContentType == CampaignContentTypeVisual {
		body = `{{ template "content" . }}`
	}

	for _, r := range regTplFuncs {
		body = r.regExp.ReplaceAllString(body, r.replace)
	}

	baseTPL, err := template.New(BaseTpl).Funcs(f).Parse(body)
	if err != nil {
		return nil, fmt.Errorf("error compiling base template: %v", err)
	}

	// If the format is markdown, convert Markdown to HTML.
	if c.ContentType == CampaignContentTypeMarkdown {
		var b bytes.Buffer
		if err := markdown.Convert([]byte(c.Body), &b); err != nil {
			return nil, err
		}
		body = b.String()
	} else {
		body = c.Body
	}

	// Compile the campaign message.
	for _, r := range regTplFuncs {
		body = r.regExp.ReplaceAllString(body, r.replace)
	}

	msgTpl, err := template.New(ContentTpl).Funcs(f).Parse(body)
	if err != nil {
		return nil, fmt.Errorf("error compiling message: %v", err)
	}

	tpl, err := baseTPL.AddParseTree(ContentTpl, msgTpl.Tree)
	if err != nil {
		return nil, fmt.Errorf("error inserting child template: %v", err)
	}
	out.Tpl = tpl

	if hasTplExpr(c.AltBody.String) {
		b := c.AltBody.String
		for _, r := range regTplFuncs {
			b = r.regExp.ReplaceAllString(b, r.replace)
		}
		bTpl, err := template.New(ContentTpl).Funcs(f).Parse(b)
		if err != nil {
			return nil, fmt.Errorf("error compiling alt plaintext message: %v", err)
		}
		out.AltBodyTpl = bTpl
	}

	// Detect and compile templated headers in a single pass. Most campaigns have
	// no templated headers, in which case this stays nil and headers are rendered
	// as-is on every message without any per-header scanning.
	var hdrTpls []map[string]*txttpl.Template
	for i, set := range c.Headers {
		for hdr, val := range set {
			if !hasTplExpr(val) {
				continue
			}
			if hdrTpls == nil {
				hdrTpls = make([]map[string]*txttpl.Template, len(c.Headers))
			}
			if hdrTpls[i] == nil {
				hdrTpls[i] = make(map[string]*txttpl.Template, len(set))
			}

			var txtFuncs map[string]any = f
			tpl, err := txttpl.New(ContentTpl).Funcs(txtFuncs).Parse(val)
			if err != nil {
				return nil, fmt.Errorf("error compiling header %q: %v", hdr, err)
			}
			hdrTpls[i][hdr] = tpl
		}
	}
	out.HeaderTpls = hdrTpls

	return out, nil
}

// TplFingerprint returns two hashes that uniquely identify the compiled state
// of a campaign. baseHash covers the shared base template (TemplateBody) and
// is the primary cache bucket key (by TemplateID); contentHash covers
// campaign-specific inputs (subject, body, alt body, headers and content
// type). A change in either hash invalidates the cached compilation.
func (c *Campaign) TplFingerprint() (baseHash, contentHash uint64) {
	base := fnv.New64a()
	io.WriteString(base, c.TemplateBody)

	content := fnv.New64a()
	io.WriteString(content, c.ContentType)
	io.WriteString(content, "\x00")
	io.WriteString(content, c.Subject)
	io.WriteString(content, "\x00")
	io.WriteString(content, c.Body)
	io.WriteString(content, "\x00")
	io.WriteString(content, c.AltBody.String)

	// Sort header names so that map iteration order doesn't perturb the hash.
	keys := make([]string, 0, len(c.Headers))
	for _, set := range c.Headers {
		for k := range set {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for i, set := range c.Headers {
		for _, k := range keys {
			if v, ok := set[k]; ok {
				fmt.Fprintf(content, "\x00%d:%s=%s", i, k, v)
			}
		}
	}

	return base.Sum64(), content.Sum64()
}

// hasTplExpr checks whether a given string has a Go template expression with {{ and  }}.
func hasTplExpr(s string) bool {
	_, after, ok := strings.Cut(s, "{{")
	return ok && strings.Contains(after, "}}")
}

// ConvertContent converts a campaign's body from one format to another,
// for example, Markdown to HTML.
func (c *Campaign) ConvertContent(from, to string) (string, error) {
	body := c.Body
	for _, r := range regTplFuncs {
		body = r.regExp.ReplaceAllString(body, r.replace)
	}

	// If the format is markdown, convert Markdown to HTML.
	var out string
	if from == CampaignContentTypeMarkdown &&
		(to == CampaignContentTypeHTML || to == CampaignContentTypeRichtext) {
		var b bytes.Buffer
		if err := markdown.Convert([]byte(c.Body), &b); err != nil {
			return out, err
		}
		out = b.String()
	} else {
		return out, errors.New("unknown formats to convert")
	}

	return out, nil
}
