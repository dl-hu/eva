package web

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"dlhu.dev/eva/internal/lobby"
	"dlhu.dev/eva/internal/store"
)

// site is the running server under test, reached the way a browser reaches it.
type site struct {
	base   string
	prefix string
	rooms  *lobby.Manager
	db     *store.DB
}

// newTestSite serves the site under prefix, over a database of its own.
func newTestSite(t *testing.T, prefix string) *site {
	t.Helper()
	rooms := lobby.NewManager()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	ts := httptest.NewServer(New(rooms, db, prefix))
	t.Cleanup(ts.Close)
	return &site{base: ts.URL + prefix, prefix: prefix, rooms: rooms, db: db}
}

// browser returns a client that keeps cookies, as one player's browser would.
func (s *site) browser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	return &http.Client{Jar: jar}
}

// TestCreateAndJoin walks the flow a player actually takes — home, a form,
// a room — at both the root and behind a path prefix.
func TestCreateAndJoin(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"", "/eva"} {
		t.Run("prefix="+prefix, func(t *testing.T) {
			t.Parallel()
			s := newTestSite(t, prefix)
			ada := s.browser(t)

			// Home offers the two ways in; each leads to a form of its own.
			home := bodyOf(t, get(t, ada, s.base+"/"))
			for _, action := range []string{"create", "join"} {
				if !strings.Contains(home, prefix+"/"+action) {
					t.Fatalf("home page does not offer %q", action)
				}
				if page := bodyOf(t, get(t, ada, s.base+"/"+action)); !strings.Contains(page, `name="name"`) {
					t.Errorf("the %s page does not ask for a name", action)
				}
			}
			if page := bodyOf(t, get(t, ada, s.base+"/join")); !strings.Contains(page, `name="code"`) {
				t.Error("the join page does not ask for a room code")
			}

			resp := postForm(t, ada, s.base+"/create", url.Values{"name": {"ada"}})
			code := strings.TrimPrefix(resp.Request.URL.Path, prefix+"/room/")
			if _, ok := s.rooms.Get(code); !ok {
				t.Fatalf("create landed on %q, want the page of a live room", resp.Request.URL)
			}
			if page := bodyOf(t, resp); !strings.Contains(page, code) || !strings.Contains(page, "ada") {
				t.Errorf("room page does not show both the code %q and the player name", code)
			}

			// A second player joins with the code however they typed it.
			bob := s.browser(t)
			resp = postForm(t, bob, s.base+"/join", url.Values{"name": {"bob"}, "code": {strings.ToLower(code)}})
			if got, want := resp.Request.URL.Path, prefix+"/room/"+code; got != want {
				t.Errorf("join landed on %q, want %q", got, want)
			}
			if page := bodyOf(t, resp); !strings.Contains(page, "bob") {
				t.Error("room page does not show the joining player's own name")
			}
		})
	}
}

// TestNameStaysOffTheURL keeps identity in the session: a room link is worth
// sharing only if passing it on does not pass on who you are.
func TestNameStaysOffTheURL(t *testing.T) {
	t.Parallel()
	s := newTestSite(t, "/eva")
	ada := s.browser(t)

	resp := postForm(t, ada, s.base+"/create", url.Values{"name": {"ada"}})
	roomURL := resp.Request.URL
	if strings.Contains(roomURL.RawQuery, "ada") {
		t.Errorf("room URL = %q, want no player name in it", roomURL)
	}

	// The same player, returning later, is still known and still named.
	if page := bodyOf(t, get(t, ada, roomURL.String())); !strings.Contains(page, "ada") {
		t.Error("returning to the room forgot the player's name")
	}
	if page := bodyOf(t, get(t, ada, s.base+"/join")); !strings.Contains(page, `value="ada"`) {
		t.Error("the join form does not offer the name the player already chose")
	}
}

// TestSharedLinkAsksWhoYouAre covers a player following someone else's link:
// the room is real, but the server has never met them.
func TestSharedLinkAsksWhoYouAre(t *testing.T) {
	t.Parallel()
	s := newTestSite(t, "/eva")
	room := s.rooms.Create("host")

	stranger := s.browser(t)
	resp := get(t, stranger, s.base+"/room/"+room.Code)
	if got, want := resp.Request.URL.Path, "/eva/join"; got != want {
		t.Fatalf("landed on %q, want the join form at %q", got, want)
	}
	if page := bodyOf(t, resp); !strings.Contains(page, `value="`+room.Code+`"`) {
		t.Error("the join form does not carry over the code from the link")
	}

	// Naming themselves is all it takes to get in.
	resp = postForm(t, stranger, s.base+"/join", url.Values{"name": {"ada"}, "code": {room.Code}})
	if got, want := resp.Request.URL.Path, "/eva/room/"+room.Code; got != want {
		t.Errorf("after giving a name, landed on %q, want %q", got, want)
	}
}

// TestUnknownCodeReturnsToJoin covers every way to name a room that is not
// there: the player lands back on the join form, told which code failed.
func TestUnknownCodeReturnsToJoin(t *testing.T) {
	t.Parallel()
	s := newTestSite(t, "/eva")
	tests := []struct {
		name string
		resp func(t *testing.T, c *http.Client) *http.Response
	}{
		{"typed into the join form", func(t *testing.T, c *http.Client) *http.Response {
			return postForm(t, c, s.base+"/join", url.Values{"name": {"bob"}, "code": {"ZZZZ"}})
		}},
		{"opened as a link", func(t *testing.T, c *http.Client) *http.Response {
			return get(t, c, s.base+"/room/ZZZZ")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := tt.resp(t, s.browser(t))
			if got, want := resp.Request.URL.Path, "/eva/join"; got != want {
				t.Errorf("landed on %q, want the join form at %q", got, want)
			}
			if page := bodyOf(t, resp); !strings.Contains(page, "ZZZZ") {
				t.Error("the join form does not tell the player which code failed")
			}
		})
	}
}

func TestWebsocketRequiresAName(t *testing.T) {
	t.Parallel()
	s := newTestSite(t, "/eva")
	room := s.rooms.Create("host")
	tests := []struct {
		name string
		path string
		want int
	}{
		{"no session", "/ws/" + room.Code, http.StatusUnauthorized},
		{"no room", "/ws/ZZZZ", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := get(t, s.browser(t), s.base+tt.path)
			if got := resp.StatusCode; got != tt.want {
				t.Errorf("GET %s = %d, want %d", tt.path, got, tt.want)
			}
		})
	}
}

// TestPrefixedLinksStayUnderPrefix guards the deployment behind dlhu.dev/eva:
// a page that links outside the prefix sends players to a 404 upstream.
func TestPrefixedLinksStayUnderPrefix(t *testing.T) {
	t.Parallel()
	s := newTestSite(t, "/eva")
	ada := s.browser(t)
	postForm(t, ada, s.base+"/create", url.Values{"name": {"ada"}})
	room := s.rooms.Create("host")

	tests := []struct {
		page  string
		links []string
	}{
		{"/", []string{"/eva/create", "/eva/join"}},
		{"/create", []string{`action="/eva/create"`, `href="/eva/"`}},
		{"/join", []string{`action="/eva/join"`, `href="/eva/"`}},
		{"/room/" + room.Code, []string{`href="/eva/"`}},
	}
	for _, tt := range tests {
		t.Run(tt.page, func(t *testing.T) {
			t.Parallel()
			page := bodyOf(t, get(t, ada, s.base+tt.page))
			for _, link := range tt.links {
				if !strings.Contains(page, link) {
					t.Errorf("page %s does not link to %q", tt.page, link)
				}
			}
		})
	}
}

// TestBarePrefixServesHome covers dlhu.dev/eva without the trailing slash.
func TestBarePrefixServesHome(t *testing.T) {
	t.Parallel()
	s := newTestSite(t, "/eva")
	resp := get(t, s.browser(t), s.base)
	if got, want := resp.Request.URL.Path, "/eva/"; got != want {
		t.Errorf("GET /eva landed on %q, want %q", got, want)
	}
}

func TestCleanName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"keeps an ordinary name", "ada", "ada"},
		{"trims surrounding space", "  ada  ", "ada"},
		{"names the unnamed", "", "anon"},
		{"names the blank", "   ", "anon"},
		{"drops control characters", "a\x00d\na", "ada"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := cleanName(tt.in); got != tt.want {
				t.Errorf("cleanName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestCleanNameLimitsLength checks the cap holds, whatever it is set to, and
// that a name is shortened rather than mangled.
func TestCleanNameLimitsLength(t *testing.T) {
	t.Parallel()
	for _, in := range []string{strings.Repeat("x", 300), strings.Repeat("é", 300)} {
		got := cleanName(in)
		if n := utf8.RuneCountInString(got); n > maxNameLen {
			t.Errorf("cleanName(%d runes) = %d runes, want at most %d", utf8.RuneCountInString(in), n, maxNameLen)
		}
		if !strings.HasPrefix(in, got) {
			t.Errorf("cleanName(%q…) = %q, want a prefix of its input", in[:8], got)
		}
	}
}

func postForm(t *testing.T, c *http.Client, url string, form url.Values) *http.Response {
	t.Helper()
	resp, err := c.PostForm(url, form)
	if err != nil {
		t.Fatalf("POST %s = %v, want no error", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func get(t *testing.T, c *http.Client, url string) *http.Response {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s = %v, want no error", url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	if got, want := resp.StatusCode, http.StatusOK; got != want {
		t.Fatalf("GET %s = %d, want %d", resp.Request.URL, got, want)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", resp.Request.URL, err)
	}
	return string(body)
}
