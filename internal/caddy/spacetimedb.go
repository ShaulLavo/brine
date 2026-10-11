package caddy

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ShaulLavo/brine/internal/plan"
)

type ClientIngress struct{ prefix, database string }

const tokenQueryKeyPattern = "(t|%74)(o|%6[fF])(k|%6[bB])(e|%65)(n|%6[eE])"

func NewManagedClientIngress(preview plan.ManagedRenderPreview) (ClientIngress, error) {
	d, _, _, err := preview.Values()
	if err != nil {
		return ClientIngress{}, err
	}
	return ClientIngress{prefix: d.SpacetimeDB.ClientPrefix(), database: d.SpacetimeDB.Database}, nil
}

// Allows checks client route and credential presence. The engine validates JWTs.
func (i ClientIngress) Allows(method, requestURI, authorization, upgrade string) bool {
	if i.prefix == "" || i.database == "" {
		return false
	}
	u, err := url.ParseRequestURI(requestURI)
	if err != nil || u.RawPath != "" || u.IsAbs() || u.Host != "" {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	if method == http.MethodGet && u.Path == i.prefix+"v1/database/"+i.database+"/subscribe" {
		return strings.EqualFold(upgrade, "websocket") && len(query["token"]) == 1 && query.Get("token") != ""
	}
	return method == http.MethodPost && u.Path == i.prefix+"v1/identity/websocket-token" && strings.HasPrefix(authorization, "Bearer ") && len(authorization) > len("Bearer ")
}

func RenderManagedPreview(preview plan.ManagedRenderPreview) ([]byte, error) {
	d, enginePort, appPort, err := preview.Values()
	if err != nil {
		return nil, err
	}
	i, err := NewManagedClientIngress(preview)
	if err != nil {
		return nil, err
	}
	domains := make([]string, len(d.Domains))
	for n, domain := range d.Domains {
		domains[n] = string(domain)
	}
	if len(domains) == 0 {
		return nil, errors.New("managed ingress preview: domains required")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Brine managed ingress preview; execution not admitted\n%s {\n\troute {\n", strings.Join(domains, ", "))
	queryValid := "{http.request.orig_uri.query}.matches('^([^%;]|%[0-9a-fA-F]{2})*$')"
	subscribePath := i.prefix + "v1/database/" + i.database + "/subscribe"
	tokenPath := i.prefix + "v1/identity/websocket-token"
	fmt.Fprintf(&b, "\t\t@subscribe {\n\t\t\tpath %s\n\t\t\tmethod GET\n\t\t\texpression {http.request.orig_uri}.matches('^%s([?].*)?$') && %s && {http.request.header.Upgrade}.lowerAscii() == 'websocket' && {http.request.uri.query.token} != '' && {http.request.orig_uri.query}.split('&').filter(pair, pair.matches('^%s(=|$)')).size() == 1\n\t\t}\n", subscribePath, regexp.QuoteMeta(subscribePath), queryValid, tokenQueryKeyPattern)
	fmt.Fprintf(&b, "\t\t@token {\n\t\t\tpath %s\n\t\t\tmethod POST\n\t\t\texpression {http.request.orig_uri}.matches('^%s([?].*)?$') && %s && {http.request.header.Authorization}.matches('^Bearer .+$')\n\t\t}\n", tokenPath, regexp.QuoteMeta(tokenPath), queryValid)
	for _, matcher := range []string{"subscribe", "token"} {
		fmt.Fprintf(&b, "\t\thandle @%s {\n\t\t\turi strip_prefix %s\n\t\t\treverse_proxy 127.0.0.1:%d\n\t\t}\n", matcher, strings.TrimSuffix(i.prefix, "/"), enginePort)
	}
	b.WriteString("\t\t@originalManaged expression {http.request.orig_uri.path}.matches('^/_brine/db(/|$)')\n\t\thandle @originalManaged {\n\t\t\trespond 403\n\t\t}\n\t\t@managed path /_brine/db /_brine/db/*\n\t\thandle @managed {\n\t\t\trespond 403\n\t\t}\n\t\thandle {\n")
	if appPort != 0 {
		fmt.Fprintf(&b, "\t\t\treverse_proxy 127.0.0.1:%d\n", appPort)
	} else {
		b.WriteString("\t\t\trespond 404\n")
	}
	b.WriteString("\t\t}\n\t}\n\theader X-Content-Type-Options nosniff\n\theader Referrer-Policy no-referrer\n}\n")
	return []byte(b.String()), nil
}
