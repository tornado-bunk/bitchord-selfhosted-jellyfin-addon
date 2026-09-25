package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type BackendType string

const (
	BackendPlex     BackendType = "plex"
	BackendJellyfin BackendType = "jellyfin"
)

type Config struct {
	Backend         BackendType
	PlexURL         string
	PlexToken       string
	JellyfinURL     string
	JellyfinAPIKey  string
	JellyfinUser    string
	JellyfinUserID  string
	Secret          string
	PublicURL       string
	Section         string
	AddonName       string
	RefreshInterval time.Duration
	Port            int
	LogLevel        slog.Level
	LogFormat       string
}

var secretPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,}$`)

var logLevels = map[string]slog.Level{
	"debug": slog.LevelDebug,
	"info":  slog.LevelInfo,
	"warn":  slog.LevelWarn,
	"error": slog.LevelError,
}

func Load(getenv func(string) string) (Config, error) {
	var problems []error
	fail := func(format string, args ...any) {
		problems = append(problems, fmt.Errorf(format, args...))
	}
	get := func(key, fallback string) string {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			return value
		}
		return fallback
	}
	required := func(key string) string {
		value := get(key, "")
		if value == "" {
			fail("%s is required", key)
		}
		return value
	}

	plexURL := strings.TrimRight(strings.TrimSpace(getenv("PLEX_URL")), "/")
	plexToken := strings.TrimSpace(getenv("PLEX_TOKEN"))
	jellyfinURL := strings.TrimRight(strings.TrimSpace(getenv("JELLYFIN_URL")), "/")
	jellyfinAPIKey := strings.TrimSpace(getenv("JELLYFIN_API_KEY"))

	hasPlex := plexURL != "" || plexToken != ""
	hasJellyfin := jellyfinURL != "" || jellyfinAPIKey != ""

	var backend BackendType
	defaultName := "Plex"
	var section string

	switch {
	case hasPlex && hasJellyfin:
		fail("cannot configure both Plex and Jellyfin, choose one")
		backend = BackendPlex
	case hasJellyfin:
		backend = BackendJellyfin
		defaultName = "Jellyfin"
		if jellyfinURL == "" {
			fail("JELLYFIN_URL is required")
		}
		if jellyfinAPIKey == "" {
			fail("JELLYFIN_API_KEY is required")
		}
		section = get("JELLYFIN_LIBRARY", get("JELLYFIN_SECTION", ""))
	default:
		backend = BackendPlex
		if plexURL == "" {
			fail("PLEX_URL is required")
		}
		if plexToken == "" {
			fail("PLEX_TOKEN is required")
		}
		section = get("PLEX_SECTION", "")
	}

	cfg := Config{
		Backend:        backend,
		PlexURL:        plexURL,
		PlexToken:      plexToken,
		JellyfinURL:    jellyfinURL,
		JellyfinAPIKey: jellyfinAPIKey,
		JellyfinUser:   get("JELLYFIN_USER", ""),
		JellyfinUserID: get("JELLYFIN_USER_ID", ""),
		Secret:         required("ADDON_SECRET"),
		PublicURL:      strings.TrimRight(required("PUBLIC_URL"), "/"),
		Section:        section,
		AddonName:      get("ADDON_NAME", defaultName),
	}

	if cfg.PlexURL != "" {
		parsed, err := url.Parse(cfg.PlexURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			fail("PLEX_URL must be an http or https URL")
		}
	}
	if cfg.JellyfinURL != "" {
		parsed, err := url.Parse(cfg.JellyfinURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			fail("JELLYFIN_URL must be an http or https URL")
		}
	}
	if cfg.PublicURL != "" {
		parsed, err := url.Parse(cfg.PublicURL)
		switch {
		case err != nil || parsed.Host == "":
			fail("PUBLIC_URL must be a full URL such as https://music.example.com")
		case parsed.Scheme != "https" && !(parsed.Scheme == "http" && parsed.Hostname() == "localhost"):
			fail("PUBLIC_URL must start with https:// unless the host is localhost")
		}
	}
	if cfg.Secret != "" && !secretPattern.MatchString(cfg.Secret) {
		fail("ADDON_SECRET must be at least 16 characters of letters, digits, '-' or '_'")
	}

	interval, err := time.ParseDuration(get("REFRESH_INTERVAL", "15m"))
	if err != nil || interval <= 0 {
		fail("REFRESH_INTERVAL must be a positive duration such as 15m")
	}
	cfg.RefreshInterval = interval

	port, err := strconv.Atoi(get("PORT", "8080"))
	if err != nil || port < 1 || port > 65535 {
		fail("PORT must be between 1 and 65535")
	}
	cfg.Port = port

	level, ok := logLevels[strings.ToLower(get("LOG_LEVEL", "info"))]
	if !ok {
		fail("LOG_LEVEL must be one of debug, info, warn, error")
	}
	cfg.LogLevel = level

	cfg.LogFormat = strings.ToLower(get("LOG_FORMAT", "text"))
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		fail("LOG_FORMAT must be text or json")
	}

	return cfg, errors.Join(problems...)
}
