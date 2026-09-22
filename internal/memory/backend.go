package memory

import (
	"sync"

	"github.com/genai-io/san/internal/secret"
	"github.com/genai-io/san/internal/setting"
)

// Env overrides resolved alongside settings (OMP/Hindsight standard names;
// settings win only when the env var is unset — secret.Resolve checks the
// environment first, which matches OMP's documented precedence where
// HINDSIGHT_* overrides hindsight.*).
const (
	envAPIURL   = "HINDSIGHT_API_URL"
	envAPIToken = "HINDSIGHT_API_TOKEN"
)

// Backend bundles the client with the bank scope derived for this working
// directory. It is created lazily per (settings, cwd) pair and is safe for
// concurrent use: immutable fields, stateless HTTP.
type Backend struct {
	Client *Client
	BankID string
}

// Enabled reports whether the memory backend should exist at all. It is
// the single gate every caller checks first — tools, auto-recall, ExtraTools
// injection — so an "off" configuration never constructs a client, opens a
// connection, or adds a schema.
func Enabled() bool {
	s := setting.DefaultIfInit()
	return s != nil && s.Memory().Enabled()
}

// Settings exposes the active memory configuration (zero value when
// settings are not loaded or memory is off).
func Settings() setting.MemorySettings {
	if s := setting.DefaultIfInit(); s != nil {
		return s.Memory()
	}
	return setting.MemorySettings{}
}

// New builds a Backend from the active settings for cwd, or nil when
// memory is disabled. URL precedence: HINDSIGHT_API_URL env > settings url
// > Hindsight standard default. The token comes from HINDSIGHT_API_TOKEN
// (env or secret store, via secret.Resolve) — it is a credential and never
// lives in settings.json.
//
// Construction performs no I/O; the first HTTP call happens on the first
// retain/recall/reflect.
func New(cwd string) *Backend {
	cfg := Settings()
	if !cfg.Enabled() {
		return nil
	}
	url := secret.Resolve(envAPIURL)
	if url == "" {
		url = cfg.ResolvedURL()
	}
	token := secret.Resolve(envAPIToken)
	return &Backend{
		Client: NewClient(url, token, nil),
		BankID: BankID(cwd, cfg.ResolvedScope()),
	}
}

// Singleton backend for the current process settings + cwd. Tools and the
// auto-recall hook share it; a settings change (backend off, scope change,
// URL change) invalidates the key so the next access rebuilds. The mutex
// makes construction single-flight; the map is bounded by the number of
// distinct (enabled) configurations touched this process.
var (
	backendMu    sync.Mutex
	backendCache = map[string]*Backend{}
)

// Get returns the shared backend for cwd, constructing it on first use.
// Returns nil when memory is disabled — callers must handle nil as "off".
// Keying by the full configuration means a /config edit during a session
// takes effect on the next operation without any global reset.
func Get(cwd string) *Backend {
	cfg := Settings()
	if !cfg.Enabled() {
		return nil
	}
	key := cfg.Backend + "\x00" + cfg.URL + "\x00" + cfg.Scope + "\x00" +
		secret.Resolve(envAPIURL) + "\x00" + secret.Resolve(envAPIToken)

	backendMu.Lock()
	defer backendMu.Unlock()
	if b, ok := backendCache[key]; ok {
		return b
	}
	// Rebuild from current settings under the lock; a disabled backend
	// never reaches here (checked above), but keep the invariant local.
	b := New(cwd)
	if len(backendCache) > 8 {
		backendCache = map[string]*Backend{}
	}
	backendCache[key] = b
	return b
}

// Reset drops the cached backend (tests and settings-reload paths).
func Reset() {
	backendMu.Lock()
	defer backendMu.Unlock()
	backendCache = map[string]*Backend{}
	ensuredBanks = sync.Map{}
}
