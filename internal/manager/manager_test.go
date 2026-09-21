package manager

import (
	"io"
	"log"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/knadh/listmonk/models"
	null "gopkg.in/volatiletech/null.v6"
)

// fakeStore implements Store with only link tracking wired up.
type fakeStore struct {
	creates atomic.Int64
	mu      sync.Mutex
}

func (s *fakeStore) NextCampaigns(_ []int64, _ []int64) ([]*models.Campaign, error) {
	return nil, nil
}
func (s *fakeStore) NextSubscribers(_, _ int) ([]models.Subscriber, error) {
	return nil, nil
}
func (s *fakeStore) GetCampaign(_ int) (*models.Campaign, error) { return nil, nil }
func (s *fakeStore) GetAttachment(_ int) (models.Attachment, error) {
	return models.Attachment{}, nil
}
func (s *fakeStore) UpdateCampaignStatus(_ int, _ string) error { return nil }
func (s *fakeStore) UpdateCampaignCounts(_, _, _, _ int) error  { return nil }
func (s *fakeStore) BlocklistSubscriber(_ int64) error          { return nil }
func (s *fakeStore) DeleteSubscriber(_ int64) error             { return nil }

func (s *fakeStore) CreateLink(url string) (string, error) {
	// Hold a brief lock to widen the race window between concurrent callers.
	s.mu.Lock()
	defer s.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	s.creates.Add(1)

	return "uu-" + url, nil
}

func newTestManager(_ *testing.T) (*Manager, *fakeStore) {
	st := &fakeStore{}
	m := New(Config{
		BatchSize:          10,
		Concurrency:        1,
		MessageRate:        1,
		IndividualTracking: true,
		LinkTrackURL:       "http://track.example/%s/%s/%s",
		ViewTrackURL:       "http://view.example/%s/%s",
		UnsubURL:           "http://unsub.example/%s/%s",
		OptinURL:           "http://optin.example/%s/%s",
		MessageURL:         "http://message.example/%s/%s",
		ArchiveURL:         "http://archive.example",
		RootURL:            "http://root.example",
	}, st, nil, log.New(io.Discard, "", 0))

	return m, st
}

func newRenderCampaign(id int) *models.Campaign {
	return &models.Campaign{
		CampaignMeta: models.CampaignMeta{CampaignID: id},
		Base:         models.Base{ID: id},
		UUID:         "camp-uuid",
		Subject:      `Hi {{ .Subscriber.Name }}`,
		ContentType:  models.CampaignContentTypeHTML,
		TemplateBody: `<html>{{ template "content" . }}</html>`,
		Body:         `<a href="{{ TrackLink "http://a.example?x=1&amp;y=2" . }}">link</a> msg={{ MessageURL . }}`,
		AltBody:      null.StringFrom("plain {{ .Subscriber.Name }}"),
		TemplateID:   null.IntFrom(42),
		Headers: models.Headers{
			{"X-Static": "static"},
			{"X-Hello": `hello {{ .Subscriber.Name }}`},
		},
	}
}

func TestCompileCampaignTpl_CacheHit(t *testing.T) {
	m, _ := newTestManager(t)

	c1 := newRenderCampaign(1)
	if err := m.CompileCampaignTpl(c1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c2 := newRenderCampaign(2)
	if err := m.CompileCampaignTpl(c2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c1.Tpl != c2.Tpl {
		t.Fatal("campaigns with identical content must share the compiled template")
	}
	if &c1.HeaderTpls[0] != &c2.HeaderTpls[0] || len(c1.HeaderTpls) != len(c2.HeaderTpls) {
		t.Fatal("campaigns must share the compiled header templates")
	}
}

func TestCompileCampaignTpl_RecompileOnContentChange(t *testing.T) {
	m, _ := newTestManager(t)

	c1 := newRenderCampaign(1)
	if err := m.CompileCampaignTpl(c1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c2 := newRenderCampaign(1)
	c2.Body = `<p>completely different body {{ .Subscriber.Name }}</p>`
	if err := m.CompileCampaignTpl(c2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c1.Tpl == c2.Tpl {
		t.Fatal("changed content must trigger a recompilation")
	}
}

func TestCompileCampaignTpl_RecompileOnBaseChange(t *testing.T) {
	m, _ := newTestManager(t)

	c1 := newRenderCampaign(1)
	if err := m.CompileCampaignTpl(c1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c2 := newRenderCampaign(1)
	c2.TemplateBody = `<body>{{ template "content" . }}</body>`
	if err := m.CompileCampaignTpl(c2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c1.Tpl == c2.Tpl {
		t.Fatal("a changed base template must trigger a recompilation")
	}
}

func TestCompileCampaignTpl_DeleteEvictsCache(t *testing.T) {
	m, _ := newTestManager(t)

	c1 := newRenderCampaign(1)
	if err := m.CompileCampaignTpl(c1); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m.DeleteTpl(42)

	c2 := newRenderCampaign(1)
	if err := m.CompileCampaignTpl(c2); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c1.Tpl == c2.Tpl {
		t.Fatal("DeleteTpl must evict cached campaign compilations")
	}
}

func TestCompileCampaignTpl_Concurrent(t *testing.T) {
	m, _ := newTestManager(t)

	const n = 50
	var wg sync.WaitGroup
	tpls := make([]*models.Campaign, n)
	errs := make(chan error, n)

	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			c := newRenderCampaign(100 + i)
			errs <- m.CompileCampaignTpl(c)
			tpls[i] = c
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	for i := 1; i < n; i++ {
		if tpls[i].Tpl != tpls[0].Tpl {
			t.Fatal("all concurrent compiles of identical content must share one template")
		}
	}
}

func TestTemplateFuncs_SharedInstance(t *testing.T) {
	m, _ := newTestManager(t)

	f1 := m.TemplateFuncs(nil)
	f2 := m.TemplateFuncs(newRenderCampaign(1))
	if reflect.ValueOf(f1).Pointer() != reflect.ValueOf(f2).Pointer() {
		t.Fatal("TemplateFuncs must return one shared FuncMap instead of per-campaign closures")
	}
}

func TestRenderMessage_EndToEnd(t *testing.T) {
	m, st := newTestManager(t)

	c := newRenderCampaign(1)
	if err := m.CompileCampaignTpl(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Header detection is done at compile time, not per render.
	if c.HeaderTpls[0] != nil {
		t.Fatal("static headers must not be compiled")
	}
	if c.HeaderTpls[1]["X-Hello"] == nil {
		t.Fatal("templated header must be compiled at load time")
	}

	sub := models.Subscriber{UUID: "sub-uuid", Email: "bob@example.com", Name: "Bob"}
	msg, err := m.NewCampaignMessage(c, sub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body := string(msg.Body())
	if !strings.Contains(body, `href="http://track.example/uu-http://a.example?x=1&amp;y=2/camp-uuid/sub-uuid"`) {
		t.Fatalf("TrackLink not rendered as expected: %s", body)
	}
	if !strings.Contains(body, "msg=http://message.example/camp-uuid/sub-uuid") {
		t.Fatalf("MessageURL not rendered as expected: %s", body)
	}
	if msg.Subject() != "Hi Bob" {
		t.Fatalf("unexpected subject: %q", msg.Subject())
	}
	if string(msg.AltBody()) != "plain Bob" {
		t.Fatalf("unexpected alt body: %q", string(msg.AltBody()))
	}

	// The link was registered with &amp; unescaped to &.
	if got := st.creates.Load(); got != 1 {
		t.Fatalf("expected exactly 1 CreateLink call, got %d", got)
	}

	// Rendering for a second subscriber must not recompile nor re-register.
	if err := m.CompileCampaignTpl(newRenderCampaign(2)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, err = m.NewCampaignMessage(c, models.Subscriber{UUID: "sub2", Email: "jane@example.com", Name: "Jane"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := st.creates.Load(); got != 1 {
		t.Fatalf("cached links must not hit the DB again, got %d CreateLink calls", got)
	}
}

func TestTrackLink_ConcurrentCoalescing(t *testing.T) {
	m, st := newTestManager(t)

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	results := make([]string, n)

	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			results[i] = m.trackLink("http://burst.example", "camp", "sub")
		}()
	}
	wg.Wait()

	if got := st.creates.Load(); got != 1 {
		t.Fatalf("expected concurrent misses to coalesce into 1 CreateLink, got %d", got)
	}
	for _, r := range results {
		if r != "http://track.example/uu-http://burst.example/camp/sub" {
			t.Fatalf("unexpected track link: %s", r)
		}
	}
}

func TestTrackLink_Disabled(t *testing.T) {
	m, st := newTestManager(t)
	m.cfg.DisableTracking = true

	if got := m.trackLink("http://x.example", "c", "s"); got != "http://x.example" {
		t.Fatalf("tracking disabled must return the raw URL, got %s", got)
	}
	if got := st.creates.Load(); got != 0 {
		t.Fatalf("expected no CreateLink calls, got %d", got)
	}
}

func TestTrackLink_UnescapesAmp(t *testing.T) {
	m, _ := newTestManager(t)

	got := m.trackLink("http://amp.example?a=1&amp;b=2", "c", "s")
	if !strings.Contains(got, "uu-http://amp.example?a=1&b=2") {
		t.Fatalf("&amp; must be unescaped before registering the link, got %s", got)
	}
}

func BenchmarkCompileCampaignTpl_Cached(b *testing.B) {
	m, _ := newTestManager(nil)
	c := newRenderCampaign(1)
	if err := m.CompileCampaignTpl(c); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := m.CompileCampaignTpl(c); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompileCampaignTpl_Uncached(b *testing.B) {
	m, _ := newTestManager(nil)
	funcs := m.TemplateFuncs(nil)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c := newRenderCampaign(1)
		if err := c.CompileTemplate(funcs); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTrackLink_Parallel(b *testing.B) {
	m, _ := newTestManager(nil)

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			m.trackLink("http://bench.example", "camp", "sub")
			i++
		}
	})
}
