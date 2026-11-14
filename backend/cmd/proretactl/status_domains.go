package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/proreta-io/proreta/backend/internal/platform/config"
)

// domainRow is one custom status page domain as `status-domains list` shows it.
type domainRow struct {
	Host       string
	State      string
	Primary    bool
	Disabled   bool
	Org, Page  string
	VerifiedAt *time.Time
}

// statusDomains runs `proretactl status-domains list [-format json|caddy|text] [-all]`.
func statusDomains(args []string, lookup config.LookupFunc, stdout io.Writer) error {
	if len(args) == 0 || args[0] != "list" {
		return errors.New("usage: proretactl status-domains list [-format json|caddy|text] [-all]")
	}
	fs := flag.NewFlagSet("status-domains list", flag.ContinueOnError)
	format := fs.String("format", "text", "output format: json, caddy or text")
	all := fs.Bool("all", false, "list every unreleased domain, not only the hosts served now (json and text)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch *format {
	case "json", "text":
	case "caddy":
		if *all {
			return errors.New("-all cannot be combined with -format caddy: only served hosts belong in an allow list")
		}
	default:
		return fmt.Errorf("unknown format %q (json, caddy or text)", *format)
	}
	cfg, err := config.LoadCtlDatabase(lookup)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := readDomains(ctx, cfg.DatabaseURL.Value(), *all)
	if err != nil {
		return err
	}
	return writeDomains(stdout, *format, rows, !*all)
}

// readDomains calls status_page_domains_export with the database URL of the api's role.
func readDomains(ctx context.Context, url string, all bool) ([]domainRow, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		// The error can echo the URL; say only what failed.
		return nil, errors.New("cannot connect to the database (check PRORETA_DATABASE_URL)")
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	rs, err := conn.Query(ctx, `SELECT host, state, is_primary, disabled, tenant_slug, page_slug, verified_at FROM status_page_domains_export($1)`, all)
	if err != nil {
		return nil, fmt.Errorf("read domains: %w", err)
	}
	defer rs.Close()
	var out []domainRow
	for rs.Next() {
		var r domainRow
		if err := rs.Scan(&r.Host, &r.State, &r.Primary, &r.Disabled, &r.Org, &r.Page, &r.VerifiedAt); err != nil {
			return nil, fmt.Errorf("read domains: %w", err)
		}
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("read domains: %w", err)
	}
	return out, nil
}

// jsonDomains is the documented, stable JSON output: an object with a "domains" array so fields
// can be added at the top level later; each entry only gains fields.
type jsonDomains struct {
	Domains []jsonDomain `json:"domains"`
}

type jsonDomain struct {
	Host         string     `json:"host"`
	State        string     `json:"state"`
	Primary      bool       `json:"primary"`
	Disabled     bool       `json:"disabled"`
	Organization string     `json:"organization"`
	Page         string     `json:"page"`
	VerifiedAt   *time.Time `json:"verifiedAt"`
}

// writeDomains prints rows (sorted by host by the database) in one format. served says the rows
// are the hosts served now.
func writeDomains(w io.Writer, format string, rows []domainRow, served bool) error {
	switch format {
	case "json":
		out := jsonDomains{Domains: make([]jsonDomain, 0, len(rows))}
		for _, r := range rows {
			d := jsonDomain{Host: r.Host, State: r.State, Primary: r.Primary, Disabled: r.Disabled, Organization: r.Org, Page: r.Page}
			if r.VerifiedAt != nil {
				v := r.VerifiedAt.UTC()
				d.VerifiedAt = &v
			}
			out.Domains = append(out.Domains, d)
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	case "caddy":
		return writeCaddy(w, rows)
	default:
		tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
		fmt.Fprintln(tw, "HOST\tSTATE\tDISABLED\tPRIMARY\tORGANIZATION\tSTATUS PAGE")
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Host, r.State, yesNo(r.Disabled), yesNo(r.Primary), r.Org, r.Page)
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		what := "domains"
		if served {
			what = "served hosts"
		}
		_, err := fmt.Fprintf(w, "%d %s\n", len(rows), what)
		return err
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// writeCaddy prints a Caddyfile fragment: the static allow list (one site block with exactly the
// served hosts, certificates for those only) and, commented out, the on-demand alternative that
// asks Proreta before each certificate. It never contains a secret: the upstream and the ask URL
// are environment placeholders the operator sets.
func writeCaddy(w io.Writer, rows []domainRow) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Proreta status pages on customer domains: %d host(s) served now.\n", len(rows))
	b.WriteString("# Generated by `proretactl status-domains list -format caddy`. Set PRORETA_STATUSPAGE_UPSTREAM to the\n")
	b.WriteString("# statuspage Service (host:port); it must receive the original Host header, which reverse_proxy keeps.\n\n")
	b.WriteString("# Static allow list: Caddy obtains certificates for exactly these hosts. Regenerate and reload after a change.\n")
	if len(rows) == 0 {
		b.WriteString("# (no hosts yet: a site block needs at least one address)\n")
	} else {
		hosts := make([]string, len(rows))
		for i, r := range rows {
			hosts[i] = r.Host
		}
		fmt.Fprintf(&b, "%s {\n\treverse_proxy {$PRORETA_STATUSPAGE_UPSTREAM}\n}\n", strings.Join(hosts, ", "))
	}
	b.WriteString("\n# On-demand TLS instead of the static list: Caddy asks Proreta before it requests a certificate, so a new\n")
	b.WriteString("# verified host needs no reload. PRORETA_TLS_ASK_URL is the internal URL of the statuspage ask listener,\n")
	b.WriteString("# for example http://statuspage:9091/internal/tls/allow?token=<token> (Caddy adds &domain=<host>).\n")
	b.WriteString("# Check the syntax against the Caddy version you run.\n")
	b.WriteString("#\n")
	b.WriteString("# {\n#\ton_demand_tls {\n#\t\task {$PRORETA_TLS_ASK_URL}\n#\t}\n# }\n")
	b.WriteString("# https:// {\n#\ttls {\n#\t\ton_demand\n#\t}\n#\treverse_proxy {$PRORETA_STATUSPAGE_UPSTREAM}\n# }\n")
	_, err := io.WriteString(w, b.String())
	return err
}
