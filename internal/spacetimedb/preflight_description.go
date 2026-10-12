package spacetimedb

import "slices"

// NativePreflightDescription is decoded review syntax. It cannot be consumed by
// PublishNativeUpdate and grants no native token or host effect authority.
type NativePreflightDescription func() nativePreflightDescription

func (d NativePreflightDescription) value() nativePreflightDescription {
	if d == nil {
		return nativePreflightDescription{}
	}
	return d()
}

type nativePreflightDescription struct {
	automatic   bool
	breaks      bool
	major       bool
	responseSHA string
	tokenSHA    string
	token       []byte
}

func DescribeNativePreflight(raw []byte) (NativePreflightDescription, error) {
	parsed, err := decodeNativePublicationPreflight(raw)
	if err != nil {
		return nil, err
	}
	token := parsed.TokenBytes()
	value := nativePreflightDescription{automatic: parsed.Automatic(), breaks: parsed.BreaksClients(), major: parsed.MajorUpgrade(), responseSHA: publicationBytesSHA256(raw), tokenSHA: publicationBytesSHA256(token), token: token}
	return NativePreflightDescription(func() nativePreflightDescription { return value }), nil
}

func (d NativePreflightDescription) Automatic() bool        { return d.value().automatic }
func (d NativePreflightDescription) MajorUpgrade() bool     { return d.value().major }
func (d NativePreflightDescription) ResponseSHA256() string { return d.value().responseSHA }
func (d NativePreflightDescription) TokenSHA256() string    { return d.value().tokenSHA }
func (d NativePreflightDescription) TokenBytes() []byte     { return slices.Clone(d.value().token) }
