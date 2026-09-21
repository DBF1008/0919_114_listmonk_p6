package manager

import (
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/knadh/listmonk/models"
	null "gopkg.in/volatiletech/null.v6"
)

// mockStore is a mock implementation of the Store interface.
type mockStore struct {
	createLinkCalls atomic.Int64
}

func (s *mockStore) NextCampaigns(currentIDs []int64, sentCounts []int64) ([]*models.Campaign, error) {
	return nil, nil
}

func (s *mockStore) NextSubscribers(campID, limit int) ([]models.Subscriber, error) {
	return nil, nil
}

func (s *mockStore) GetCampaign(campID int) (*models.Campaign, error) {
	return nil, nil
}

func (s *mockStore) GetAttachment(mediaID int) (models.Attachment, error) {
	return models.Attachment{}, nil
}

func (s *mockStore) UpdateCampaignStatus(campID int, status string) error {
	return nil
}

func (s *mockStore) UpdateCampaignCounts(campID int, toSend int, sent int, lastSubID int) error {
	return nil
}

func (s *mockStore) CreateLink(url string) (string, error) {
	s.createLinkCalls.Add(1)
	return fmt.Sprintf("uuid-%d", len(url)), nil
}

func (s *mockStore) BlocklistSubscriber(id int64) error {
	return nil
}

func (s *mockStore) DeleteSubscriber(id int64) error {
	return nil
}

func newTestManager(store Store) *Manager {
	return New(Config{
		BatchSize:    10,
		Concurrency:  1,
		MessageRate:  100,
		LinkTrackURL: "http://track/%s/%s/%s",
		MessageURL:   "http://msg/%s/%s",
		UnsubURL:     "http://unsub/%s/%s",
	}, store, nil, log.New(io.Discard, "", 0))
}

func newTestCampaign(id, tplID int) *models.Campaign {
	return &models.Campaign{
		UUID:         fmt.Sprintf("camp-uuid-%d", id),
		Subject:      "Test subject",
		FromEmail:    "test@example.com",
		Body:         "<p>hello {{ .Subscriber.Email }}</p>",
		ContentType:  models.CampaignContentTypeHTML,
		TemplateBody: `<html><body>{{ template "content" . }}</body></html>`,
		TemplateID:   null.IntFrom(tplID),
	}
}

func TestCompileCampaignTplCachesByTemplateID(t *testing.T) {
	m := newTestManager(&mockStore{})

	c1 := newTestCampaign(1, 1)
	if err := m.CompileCampaignTpl(c1); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}

	// A different campaign using the same template with the same content
	// must reuse the cached compilation.
	c2 := newTestCampaign(2, 1)
	if err := m.CompileCampaignTpl(c2); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}
	if c1.Tpl != c2.Tpl {
		t.Fatal("expected cached compiled template to be reused")
	}

	// Changed content must invalidate the cache and trigger a recompile.
	c3 := newTestCampaign(3, 1)
	c3.Body = "<p>changed {{ .Subscriber.Email }}</p>"
	if err := m.CompileCampaignTpl(c3); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}
	if c3.Tpl == c1.Tpl {
		t.Fatal("expected template to be recompiled after content change")
	}

	// The recompiled result must be cached for subsequent campaigns.
	c4 := newTestCampaign(4, 1)
	c4.Body = c3.Body
	if err := m.CompileCampaignTpl(c4); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}
	if c4.Tpl != c3.Tpl {
		t.Fatal("expected recompiled template to be cached")
	}
}

func TestCompileCampaignTplCachesHeaders(t *testing.T) {
	m := newTestManager(&mockStore{})

	// Campaign without templated headers must not allocate header templates.
	c1 := newTestCampaign(1, 1)
	c1.Headers = models.Headers{{"X-Static": "value"}}
	if err := m.CompileCampaignTpl(c1); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}
	if c1.HeaderTpls != nil {
		t.Fatal("expected HeaderTpls to be nil for non-templated headers")
	}

	// The header scan result must be cached and reused.
	c2 := newTestCampaign(2, 1)
	c2.Headers = models.Headers{{"X-Static": "value"}}
	if err := m.CompileCampaignTpl(c2); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}
	if c2.HeaderTpls != nil {
		t.Fatal("expected cached HeaderTpls to be nil for non-templated headers")
	}

	// Templated headers must be compiled once and shared from the cache.
	c3 := newTestCampaign(3, 1)
	c3.Headers = models.Headers{{"X-Tpl": "hi {{ .Subscriber.Name }}"}}
	if err := m.CompileCampaignTpl(c3); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}
	if c3.HeaderTpls == nil || c3.HeaderTpls[0]["X-Tpl"] == nil {
		t.Fatal("expected templated header to be compiled")
	}

	c4 := newTestCampaign(4, 1)
	c4.Headers = models.Headers{{"X-Tpl": "hi {{ .Subscriber.Name }}"}}
	if err := m.CompileCampaignTpl(c4); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}
	if c4.HeaderTpls[0]["X-Tpl"] != c3.HeaderTpls[0]["X-Tpl"] {
		t.Fatal("expected cached header template to be reused")
	}
}

func TestDeleteTplEvictsCampaignTplCache(t *testing.T) {
	m := newTestManager(&mockStore{})

	c1 := newTestCampaign(1, 1)
	if err := m.CompileCampaignTpl(c1); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}

	m.DeleteTpl(1)

	c2 := newTestCampaign(2, 1)
	if err := m.CompileCampaignTpl(c2); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}
	if c2.Tpl == c1.Tpl {
		t.Fatal("expected cache entry to be evicted on DeleteTpl")
	}
}

func TestTemplateFuncsSharedAndStateless(t *testing.T) {
	m := newTestManager(&mockStore{})

	// Funcs must be shared across campaigns and safe to get with a nil campaign.
	f1 := m.TemplateFuncs(nil)
	f2 := m.TemplateFuncs(newTestCampaign(1, 1))
	if fmt.Sprintf("%p", f1["MessageURL"]) != fmt.Sprintf("%p", f2["MessageURL"]) {
		t.Fatal("expected template funcs to be shared across campaigns")
	}

	// MessageURL must derive the campaign UUID from the message, not from
	// a captured campaign, so that cached templates render correctly.
	msgURL, ok := f1["MessageURL"].(func(*CampaignMessage) string)
	if !ok {
		t.Fatal("MessageURL has unexpected signature")
	}
	msg := &CampaignMessage{
		Campaign:   &models.Campaign{UUID: "camp-abc"},
		Subscriber: models.Subscriber{UUID: "sub-xyz"},
	}
	if got := msgURL(msg); got != "http://msg/camp-abc/sub-xyz" {
		t.Fatalf("unexpected MessageURL: %s", got)
	}
}

func TestTrackLinkCachesLinks(t *testing.T) {
	store := &mockStore{}
	m := newTestManager(store)

	const url = "https://example.com/page"
	want := "uuid-24" // len(url)

	if got := m.trackLink(url, "camp", "sub"); got != fmt.Sprintf("http://track/%s/camp/sub", want) {
		t.Fatalf("unexpected tracked link: %s", got)
	}

	// Concurrent lookups of the same URL must hit the cache and not
	// register the link again.
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.trackLink(url, "camp", "sub")
		}()
	}
	wg.Wait()

	if n := store.createLinkCalls.Load(); n != 1 {
		t.Fatalf("expected CreateLink to be called once, got %d", n)
	}
}

func TestTrackLinkDisabled(t *testing.T) {
	m := newTestManager(&mockStore{})
	m.cfg.DisableTracking = true

	const url = "https://example.com/page"
	if got := m.trackLink(url, "camp", "sub"); got != url {
		t.Fatalf("expected original URL when tracking is disabled, got %s", got)
	}
}

func TestNewCampaignMessageRenders(t *testing.T) {
	m := newTestManager(&mockStore{})

	c := newTestCampaign(1, 1)
	c.Subject = "Hi {{ .Subscriber.Name }}"
	c.Headers = models.Headers{
		{"X-Static": "value"},
		{"X-Tpl": "city-{{ .Subscriber.Attribs.city }}"},
	}
	if err := m.CompileCampaignTpl(c); err != nil {
		t.Fatalf("error compiling template: %v", err)
	}

	sub := models.Subscriber{
		UUID:    "sub-uuid",
		Email:   "sub@example.com",
		Name:    "Test Sub",
		Attribs: models.JSON{"city": "Bengaluru"},
	}
	msg, err := m.NewCampaignMessage(c, sub)
	if err != nil {
		t.Fatalf("error rendering message: %v", err)
	}

	if msg.Subject() != "Hi Test Sub" {
		t.Fatalf("unexpected subject: %s", msg.Subject())
	}
	if !strings.Contains(string(msg.Body()), "hello sub@example.com") {
		t.Fatalf("unexpected body: %s", msg.Body())
	}
	if msg.headers[0]["X-Static"] != "value" {
		t.Fatalf("unexpected static header: %v", msg.headers[0])
	}
	if msg.headers[1]["X-Tpl"] != "city-Bengaluru" {
		t.Fatalf("unexpected templated header: %v", msg.headers[1])
	}
}
