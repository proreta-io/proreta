package mail

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"net/url"
	"slices"
	"strings"
	texttemplate "text/template"
	"unicode"
	"unicode/utf8"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// maxParamRunes caps one parameter; names of organizations and people are shorter.
const maxParamRunes = 200

// kindSpec describes one template: its subject, and, for a message that carries a link, the
// path (with the fragment marker) the token is appended to.
type kindSpec struct {
	subject string
	// linkPath is "" for a message without a link. The token goes after it, in the URL
	// fragment: a fragment is never sent to a server, so it stays out of proxy and access logs.
	linkPath string
}

var specs = map[Kind]kindSpec{
	KindTest:        {subject: "Proreta test message"},
	KindVerifyEmail: {subject: "Confirm your email address for Proreta", linkPath: "/verify-email#"},
}

// Kinds returns every kind that has a template, in a stable order.
func Kinds() []Kind {
	out := make([]Kind, 0, len(specs))
	for k := range specs {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Known reports whether kind has a template.
func Known(kind Kind) bool { _, ok := specs[kind]; return ok }

type compiled struct {
	subject *texttemplate.Template
	text    *texttemplate.Template
	html    *htmltemplate.Template
}

var compiledTemplates = func() map[Kind]compiled {
	out := make(map[Kind]compiled, len(specs))
	for kind := range specs {
		t := texttemplate.Must(texttemplate.ParseFS(templateFS, "templates/"+string(kind)+".txt.tmpl"))
		h := htmltemplate.Must(htmltemplate.ParseFS(templateFS, "templates/layout.html.tmpl", "templates/"+string(kind)+".html.tmpl"))
		out[kind] = compiled{subject: texttemplate.Must(texttemplate.New("subject").Parse(specs[kind].subject)), text: t, html: h}
	}
	return out
}()

// view is what the templates see. HTML templates escape every field; the link is only ever
// built here, from the configured public URL and a token, never taken from a parameter.
type view struct {
	Subject    string
	PublicHost string
	Link       string
	// Params are the cleaned values the producer stored (organization name, inviter, ...).
	Params map[string]string
}

// Render builds the message of kind for to. publicURL is PRORETA_PUBLIC_URL; the token, when the
// kind has a link, is params["token"] and is not exposed to the templates on its own.
func Render(kind Kind, publicURL, to string, params map[string]string) (Message, error) {
	spec, ok := specs[kind]
	if !ok {
		return Message{}, fmt.Errorf("mail: unknown kind %q", kind)
	}
	c := compiledTemplates[kind]
	v := view{Params: make(map[string]string, len(params))}
	if u, err := url.Parse(publicURL); err == nil && u.Host != "" {
		v.PublicHost = u.Host
	} else {
		v.PublicHost = "Proreta"
	}
	for k, val := range params {
		if k != "token" {
			v.Params[k] = cleanParam(val)
		}
	}
	if spec.linkPath != "" {
		token := params["token"]
		if token == "" || strings.ContainsAny(token, "#?&/ \r\n") {
			return Message{}, errors.New("mail: missing or malformed token")
		}
		v.Link = strings.TrimSuffix(publicURL, "/") + spec.linkPath + token
	}
	var subject, text, html bytes.Buffer
	if err := c.subject.Execute(&subject, v); err != nil {
		return Message{}, fmt.Errorf("mail: render subject: %w", err)
	}
	v.Subject = subject.String()
	if err := c.text.Execute(&text, v); err != nil {
		return Message{}, fmt.Errorf("mail: render text: %w", err)
	}
	if err := c.html.ExecuteTemplate(&html, "layout.html.tmpl", v); err != nil {
		return Message{}, fmt.Errorf("mail: render html: %w", err)
	}
	return Message{To: to, Subject: v.Subject, Text: text.String(), HTML: html.String()}, nil
}

// cleanParam makes a user-supplied name safe for a subject line and a text body: control and
// invisible formatting characters (line breaks, bidi overrides) are dropped, whitespace is
// collapsed and the length is capped.
func cleanParam(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(b.String()), " ")
	if utf8.RuneCountInString(out) > maxParamRunes {
		out = string([]rune(out)[:maxParamRunes]) + "…"
	}
	return out
}
