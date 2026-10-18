package automation

import "fmt"

// Migrate parses a v1 or v2 document strictly, validates it and returns it
// as a v2 document together with the version it was read as.
//
// The upgrade is purely additive and deterministic: schema becomes
// SchemaIDV2, schema_version is set to 2 and an empty runtime becomes
// "device". Every other field is kept as Parse returns it (params
// normalised, defaults filled), so Migrate(x) minus those three fields equals
// Parse(x), and migrating a migrated document changes nothing.
//
// A document that does not parse or validate returns its *ValidationResult as
// the error; fromVersion is 0 when the schema id is unknown or the JSON is
// invalid.
func Migrate(doc []byte) (*Definition, int, error) {
	a, res := decodeStrict(doc)
	if res != nil {
		return nil, 0, res
	}
	from := a.Version()
	if from == 0 {
		return nil, 0, fmt.Errorf("automation: unsupported schema %q", a.Schema)
	}
	if res := ValidateDocument(a); !res.Valid() {
		return nil, from, res
	}
	upgrade(a)
	return a, from, nil
}

// Upgrade returns a v2 copy of a v1 or v2 document (see Migrate). It does not
// validate; a nil document returns nil.
func Upgrade(a *Automation) *Definition {
	if a == nil {
		return nil
	}
	c := a.Clone()
	upgrade(c)
	return c
}

func upgrade(a *Automation) {
	a.Schema = SchemaIDV2
	a.SchemaVersion = SchemaVersionV2
	a.Runtime = a.EffectiveRuntime()
}
