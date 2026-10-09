package spec

// ValidateRouting checks the syntax needed to render an existing route, without
// authorizing a deployment. In particular it does not consult registry, secret,
// resource or domain allowlists. Ownership must be established by the caller.
func ValidateRouting(name Name, domains []Domain, port Port) error {
	if len(name) > 63 || !label.MatchString(string(name)) {
		return refusal("spec.invalid_name", "name", "expected a lowercase DNS label of 1 to 63 bytes")
	}
	if port < 1024 || port > 65535 {
		return refusal("spec.invalid_port", "port", "port must be between 1024 and 65535")
	}
	if len(domains) == 0 {
		return refusal("spec.invalid_domain", "domains", "at least one domain is required")
	}
	seen := map[Domain]bool{}
	for _, domain := range domains {
		if !isASCII(string(domain)) || !validDomain(string(domain)) || seen[domain] {
			return refusal("spec.invalid_domain", "domains", "expected unique canonical ASCII DNS domains without IDN labels")
		}
		seen[domain] = true
	}
	return nil
}
