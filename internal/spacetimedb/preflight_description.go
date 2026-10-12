package spacetimedb

import "slices"

// NativePreflightDescription is decoded review syntax. It cannot be consumed by
// PublishNativeUpdate and grants no native token or host effect authority.
type NativePreflightDescription struct {
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
		return NativePreflightDescription{}, err
	}
	token := parsed.TokenBytes()
	return NativePreflightDescription{automatic: parsed.Automatic(), breaks: parsed.BreaksClients(), major: parsed.MajorUpgrade(), responseSHA: publicationBytesSHA256(raw), tokenSHA: publicationBytesSHA256(token), token: token}, nil
}

func (d NativePreflightDescription) Automatic() bool            { return d.automatic }
func (d NativePreflightDescription) BreaksClients() bool        { return d.breaks }
func (d NativePreflightDescription) MajorUpgrade() bool         { return d.major }
func (d NativePreflightDescription) ResponseSHA256() string     { return d.responseSHA }
func (d NativePreflightDescription) TokenSHA256() string        { return d.tokenSHA }
func (d NativePreflightDescription) TokenBytes() []byte         { return slices.Clone(d.token) }
func (NativePreflightDescription) String() string               { return "[private native preflight description]" }
func (NativePreflightDescription) GoString() string             { return "[private native preflight description]" }
func (NativePreflightDescription) MarshalJSON() ([]byte, error) { return nil, ErrPrivateCredential }
