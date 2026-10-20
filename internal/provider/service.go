// Package provider implements the Ollama Cloud auth, model, executor, and quota logic behind
// the plugin ABI. Everything that talks to ollama.com goes through a transport.Host so the
// CLIProxyAPI host keeps control of proxies and request logging.
package provider

import (
	"strings"
	"sync"
	"time"

	"github.com/azwildfire/cliproxyapi-ollama-plugin/internal/transport"
)

// ProviderID is the CLIProxyAPI provider key for Ollama Cloud credentials and models.
const ProviderID = "ollama-cloud"

const userAgentPrefix = "CLIProxyAPI-Ollama-Plugin"

// Service holds plugin state shared by every host call.
type Service struct {
	host transport.Host
	now  func() time.Time

	configMu sync.RWMutex
	config   Config
	version  string

	loginMu sync.Mutex
	logins  map[string]*loginSession

	keyMu sync.Mutex
	keys  map[string]*deviceKey

	modelMu      sync.Mutex
	modelEntries map[string]modelCacheEntry
	showEntries  map[string]showCacheEntry

	quotaMu      sync.Mutex
	quotaEntries map[string]quotaCacheEntry
}

// New creates a Service bound to the given host transport.
func New(host transport.Host) *Service {
	return &Service{
		host:         host,
		now:          time.Now,
		config:       DefaultConfig(),
		version:      "dev",
		logins:       make(map[string]*loginSession),
		keys:         make(map[string]*deviceKey),
		modelEntries: make(map[string]modelCacheEntry),
		showEntries:  make(map[string]showCacheEntry),
		quotaEntries: make(map[string]quotaCacheEntry),
	}
}

// SetVersion records the plugin version used in the User-Agent header.
func (s *Service) SetVersion(version string) {
	version = strings.TrimSpace(version)
	if version == "" {
		return
	}
	s.configMu.Lock()
	s.version = version
	s.configMu.Unlock()
}

// Configure replaces the active configuration and drops derived caches.
func (s *Service) Configure(raw []byte) error {
	cfg, errParse := ParseConfig(raw)
	if errParse != nil {
		return errParse
	}
	s.configMu.Lock()
	s.config = cfg
	s.configMu.Unlock()
	s.modelMu.Lock()
	clear(s.modelEntries)
	s.modelMu.Unlock()
	s.quotaMu.Lock()
	clear(s.quotaEntries)
	s.quotaMu.Unlock()
	return nil
}

// Config returns a copy of the active configuration.
func (s *Service) Config() Config {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config
}

func (s *Service) userAgent() string {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return userAgentPrefix + "/" + s.version
}

// Shutdown releases in-memory sessions and caches.
func (s *Service) Shutdown() {
	s.loginMu.Lock()
	clear(s.logins)
	s.loginMu.Unlock()
	s.keyMu.Lock()
	clear(s.keys)
	s.keyMu.Unlock()
	s.modelMu.Lock()
	clear(s.modelEntries)
	clear(s.showEntries)
	s.modelMu.Unlock()
	s.quotaMu.Lock()
	clear(s.quotaEntries)
	s.quotaMu.Unlock()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
