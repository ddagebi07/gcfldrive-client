package guard

// DefaultConfig is the administrator password every PC gets unless install is
// run with --set-password. Only a salted PBKDF2 hash is kept in the source, never
// the password itself.
func DefaultConfig() *Config {
	return &Config{
		Salt:       "YLwtTjmBAiqeN8dUcsLH9g==",
		Hash:       "0fL7DWxGRLRle1jfyKHIVRiq9075J8D929/gGx3ONsQ=",
		Iterations: 210000,
	}
}
