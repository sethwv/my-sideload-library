package config

import "testing"

func TestLoad_AllowsGeneratedCredentials(t *testing.T) {
	t.Setenv("LIBRARY_PATH", "/books")
	t.Setenv("SESSION_SECRET", "")

	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.SessionSecret != "" {
		t.Errorf("SessionSecret = %q, want empty", c.SessionSecret)
	}
	if c.LogLevel != "info" || c.LogHTTP {
		t.Errorf("logging defaults = (%q, %t), want (info, false)", c.LogLevel, c.LogHTTP)
	}
}

func TestDataDir_DefaultAndOverride(t *testing.T) {
	t.Setenv("DATA_DIR", "")
	if got := DataDir(); got != "/data" {
		t.Errorf("DataDir() = %q, want /data", got)
	}
	t.Setenv("DATA_DIR", "/state")
	if got := DataDir(); got != "/state" {
		t.Errorf("DataDir() = %q, want /state", got)
	}
}

func TestLoad_LoggingDefaultsAndOverrides(t *testing.T) {
	t.Setenv("LIBRARY_PATH", "/books")
	t.Setenv("LOG_LEVEL", "DEBUG")
	t.Setenv("LOG_HTTP", "true")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.LogLevel != "DEBUG" || !c.LogHTTP {
		t.Fatalf("logging config = (%q, %t), want (DEBUG, true)", c.LogLevel, c.LogHTTP)
	}
}

func TestLoad_InvalidLoggingConfiguration(t *testing.T) {
	t.Setenv("LIBRARY_PATH", "/books")
	t.Setenv("LOG_LEVEL", "verbose")
	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded with invalid LOG_LEVEL")
	}
	t.Setenv("LOG_LEVEL", "")
	t.Setenv("LOG_HTTP", "sometimes")
	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded with invalid LOG_HTTP")
	}
}
