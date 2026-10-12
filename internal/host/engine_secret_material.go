package host

import "errors"

type EngineSecretMaterial struct{ values [4][]byte }

func NewEngineSecretMaterial(publisher, signingPrivate, signingPublic, authPolicy []byte) (*EngineSecretMaterial, error) {
	values := [4][]byte{publisher, signingPrivate, signingPublic, authPolicy}
	for _, value := range values {
		if len(value) == 0 || len(value) > 32<<10 {
			return nil, errors.New("engine material refused")
		}
	}
	if len(publisher) > 16<<10 {
		return nil, errors.New("engine material refused")
	}
	material := &EngineSecretMaterial{}
	for i, value := range values {
		material.values[i] = append([]byte(nil), value...)
	}
	return material, nil
}

func (m *EngineSecretMaterial) Clear() {
	if m == nil {
		return
	}
	for i := range m.values {
		clear(m.values[i])
		m.values[i] = nil
	}
}

func (*EngineSecretMaterial) String() string   { return "[protected engine material]" }
func (*EngineSecretMaterial) GoString() string { return "[protected engine material]" }
func (*EngineSecretMaterial) MarshalJSON() ([]byte, error) {
	return nil, errors.New("engine material is private")
}
