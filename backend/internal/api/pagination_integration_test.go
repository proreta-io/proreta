package api_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/proreta-io/proreta/backend/internal/testsupport/pgtest"
)

// walk follows nextCursor through a list and returns the ids per page.
func walk(t *testing.T, c *client, path, idKey string, limit int, hdr map[string]string) [][]string {
	t.Helper()
	var pages [][]string
	cursor := ""
	for {
		q := fmt.Sprintf("%s?limit=%d", path, limit)
		if cursor != "" {
			q += "&cursor=" + url.QueryEscape(cursor)
		}
		r := c.do(http.MethodGet, q, "", hdr)
		if r.code != http.StatusOK {
			t.Fatalf("%s: %d %s", q, r.code, r.raw)
		}
		items, _ := r.body["items"].([]any)
		var ids []string
		for _, it := range items {
			ids = append(ids, it.(map[string]any)[idKey].(string))
		}
		pages = append(pages, ids)
		next, present := r.body["nextCursor"]
		if !present {
			t.Fatalf("nextCursor missing: %s", r.raw)
		}
		if next == nil {
			return pages
		}
		cursor = next.(string)
		if len(pages) > 20 {
			t.Fatal("paging does not end")
		}
	}
}

func rejected(t *testing.T, c *client, path string, hdr map[string]string) {
	t.Helper()
	r := c.do(http.MethodGet, path, "", hdr)
	if r.code != http.StatusBadRequest || !strings.HasPrefix(r.header.Get("Content-Type"), "application/problem+json") {
		t.Errorf("GET %s: %d %s, want 400 problem+json", path, r.code, r.raw)
	}
}

func TestIntegrationListPagination(t *testing.T) {
	env := pgtest.New(t, pgtest.DBName("apipagination"))
	c := newRegistrationServer(t, env, membersOpts(), false, &lockedBuffer{})
	adm := adminSession(c)

	// Monitors: 5, one paused, so the summary (4 unknown, 1 paused) is the same on every page.
	var created []string
	for i := 0; i < 5; i++ {
		r := c.do(http.MethodPost, "/api/v1/monitors", fmt.Sprintf(`{"type":"http","name":"m%d","http":{"url":"https://example.com/%d"}}`, i, i), adm)
		if r.code != http.StatusCreated {
			t.Fatalf("create: %d %s", r.code, r.raw)
		}
		created = append(created, r.body["id"].(string))
	}
	if r := c.do(http.MethodPost, "/api/v1/monitors/"+created[0]+"/pause", "", adm); r.code >= 300 {
		t.Fatalf("pause: %d %s", r.code, r.raw)
	}
	pages := walk(t, c, "/api/v1/monitors", "id", 2, adm)
	if len(pages) != 3 || len(pages[0]) != 2 || len(pages[1]) != 2 || len(pages[2]) != 1 {
		t.Fatalf("pages = %v", pages)
	}
	var all []string
	for _, p := range pages {
		all = append(all, p...)
	}
	for i := range created { // created oldest first, ids are UUIDv7: the order is the creation order
		if all[i] != created[i] {
			t.Fatalf("order %v, want %v", all, created)
		}
	}
	for _, q := range []string{"?limit=2", "?limit=1"} {
		r := c.do(http.MethodGet, "/api/v1/monitors"+q, "", adm)
		sum, _ := r.body["summary"].(map[string]any)
		if sum["paused"] != 1.0 || sum["unknown"] != 4.0 || sum["up"] != 0.0 || sum["down"] != 0.0 {
			t.Errorf("%s summary %v counts the page", q, sum)
		}
	}
	// Default size: everything fits, no next page. Past the end: an empty page.
	r := c.do(http.MethodGet, "/api/v1/monitors", "", adm)
	if items, _ := r.body["items"].([]any); len(items) != 5 || r.body["nextCursor"] != nil {
		t.Fatalf("default page: %s", r.raw)
	}

	// Members: the admin plus three invited people.
	for i := 0; i < 3; i++ {
		tok, _ := invite(t, c, adm, "member")
		joinByRegister(t, c, tok, fmt.Sprintf("page%d@example.com", i))
	}
	mp := walk(t, c, "/api/v1/members", "userId", 3, adm)
	if len(mp) != 2 || len(mp[0]) != 3 || len(mp[1]) != 1 {
		t.Fatalf("member pages = %v", mp)
	}
	if seen := map[string]bool{mp[0][0]: true, mp[0][1]: true, mp[0][2]: true, mp[1][0]: true}; len(seen) != 4 {
		t.Fatalf("a member repeats: %v", mp)
	}

	// A cursor this API did not issue, an empty one, and a limit out of range are 400.
	for _, path := range []string{"/api/v1/monitors", "/api/v1/members"} {
		good := c.do(http.MethodGet, path+"?limit=1", "", adm).body["nextCursor"].(string)
		for _, bad := range []string{"garbage", good + "x", good[1:], "e30"} {
			rejected(t, c, path+"?cursor="+url.QueryEscape(bad), adm)
		}
		rejected(t, c, path+"?cursor=", adm)
		rejected(t, c, path+"?limit=0", adm)
		rejected(t, c, path+"?limit=201", adm)
	}
}
