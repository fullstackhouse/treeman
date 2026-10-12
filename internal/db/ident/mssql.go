package ident

// ValidateMSSQL reports whether s is safe to interpolate into a
// bracket-quoted SQL Server identifier.
func ValidateMSSQL(s string) error { return validate(s) }

// QuoteMSSQL returns s wrapped in brackets, or an error if s is not a
// valid identifier. Brackets rather than double quotes because
// QUOTED_IDENTIFIER is a session setting and brackets work either way.
func QuoteMSSQL(s string) (string, error) {
	if err := validate(s); err != nil {
		return "", err
	}
	return "[" + s + "]", nil
}
