package core

// Logger receives diagnostic messages. Implementations must never log request
// bodies or headers: they carry card data and credentials.
type Logger interface {
	Debug(message string, meta map[string]any)
	Info(message string, meta map[string]any)
	Error(message string, err error, meta map[string]any)
}

// NopLogger discards every message. It is the default when no logger is configured.
type NopLogger struct{}

func (NopLogger) Debug(string, map[string]any)        {}
func (NopLogger) Info(string, map[string]any)         {}
func (NopLogger) Error(string, error, map[string]any) {}
