package caddy

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ShaulLavo/brine/internal/plan"
)

type ClientIngress struct{ prefix, database string }

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
	if method == http.MethodGet && u.Path == i.prefix+"v1/database/"+i.database+"/subscribe" {
		return strings.EqualFold(upgrade, "websocket") && len(u.Query()["token"]) == 1 && u.Query().Get("token") != ""
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
	fmt.Fprintf(&b, "\t\t@subscribe {\n\t\t\tpath %sv1/database/%s/subscribe\n\t\t\tmethod GET\n\t\t\theader Upgrade websocket\n\t\t\texpression {http.request.uri.query.token} != ''\n\t\t}\n", i.prefix, i.database)
	fmt.Fprintf(&b, "\t\t@token {\n\t\t\tpath %sv1/identity/websocket-token\n\t\t\tmethod POST\n\t\t\texpression {http.request.header.Authorization}.matches('^Bearer .+$')\n\t\t}\n", i.prefix)
	for _, matcher := range []string{"subscribe", "token"} {
		fmt.Fprintf(&b, "\t\thandle @%s {\n\t\t\turi strip_prefix %s\n\t\t\treverse_proxy 127.0.0.1:%d\n\t\t}\n", matcher, strings.TrimSuffix(i.prefix, "/"), enginePort)
	}
	b.WriteString("\t\t@managed path /_brine/db /_brine/db/*\n\t\thandle @managed {\n\t\t\trespond 403\n\t\t}\n\t\thandle {\n")
	if appPort != 0 {
		fmt.Fprintf(&b, "\t\t\treverse_proxy 127.0.0.1:%d\n", appPort)
	} else {
		b.WriteString("\t\t\trespond 404\n")
	}
	b.WriteString("\t\t}\n\t}\n\theader X-Content-Type-Options nosniff\n\theader Referrer-Policy no-referrer\n}\n")
	return []byte(b.String()), nil
}
