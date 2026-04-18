// Package routing provides phone-to-agent resolution for multi-user isolation.
// Each registered phone maps to an OpenClaw agent with its own workspace,
// sessions directory, and memory — ensuring complete data isolation.
package routing

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// UserEntry is one record in the user registry JSON.
type UserEntry struct {
	Name  string `json:"name"`
	Dir   string `json:"dir"`
	Agent string `json:"agent,omitempty"` // OpenClaw agent name; derived from phone if empty
}

// Router resolves sender phone numbers to OpenClaw agent names and paths.
type Router struct {
	mu           sync.RWMutex
	registry     map[string]UserEntry // normalized phone → entry
	agentsBase   string               // e.g. ~/.openclaw/agents
	defaultAgent string               // fallback agent for unknown phones
	registryPath string               // path to registry.json (for reload)
}

// Config holds routing configuration from the bridge TOML.
type Config struct {
	RegistryPath string `toml:"registry"`
	AgentsBase   string `toml:"agents_base"`
	DefaultAgent string `toml:"default_agent"`
}

// New creates a Router from config. If the registry file doesn't exist,
// the router operates in passthrough mode (all phones → default agent).
func New(cfg Config) *Router {
	r := &Router{
		registry:     make(map[string]UserEntry),
		agentsBase:   cfg.AgentsBase,
		defaultAgent: cfg.DefaultAgent,
		registryPath: cfg.RegistryPath,
	}

	if cfg.RegistryPath != "" {
		if err := r.load(cfg.RegistryPath); err != nil {
			log.Printf("routing: failed to load registry %s: %v (passthrough mode)", cfg.RegistryPath, err)
		}
	}

	return r
}

func (r *Router) load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Registry format: { "+5492615562747": { "name": "Emanuel", "dir": "5492615562747" } }
	var raw map[string]UserEntry
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse registry: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.registry = make(map[string]UserEntry, len(raw))
	for phone, entry := range raw {
		normalized := normalize(phone)
		if entry.Agent == "" {
			entry.Agent = "user-" + normalized
		}
		r.registry[normalized] = entry
	}

	log.Printf("routing: loaded %d user(s) from %s", len(r.registry), path)
	return nil
}

// Reload re-reads the registry file. Safe for concurrent use.
func (r *Router) Reload() error {
	if r.registryPath == "" {
		return nil
	}
	return r.load(r.registryPath)
}

// AgentFor returns the OpenClaw agent name for the given phone.
// Returns the default agent if the phone is not registered.
func (r *Router) AgentFor(phone string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	n := normalize(phone)
	if entry, ok := r.registry[n]; ok {
		return entry.Agent
	}
	return r.defaultAgent
}

// SessionKeyFor returns the session key to use for a given phone.
// Format: "<agent>-wa-<phone>" for isolated sessions.
func (r *Router) SessionKeyFor(phone string) string {
	agent := r.AgentFor(phone)
	n := normalize(phone)
	return agent + "-wa-" + n
}

// SessionsJSONFor returns the path to sessions.json for the agent
// handling the given phone.
func (r *Router) SessionsJSONFor(phone string) string {
	agent := r.AgentFor(phone)
	return filepath.Join(r.agentsBase, agent, "sessions", "sessions.json")
}

// IsRegistered returns true if the phone has a dedicated agent.
func (r *Router) IsRegistered(phone string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n := normalize(phone)
	_, ok := r.registry[n]
	return ok
}

// normalize strips non-digits and canonicalizes Argentinian mobile numbers.
//
// Meta's WhatsApp Cloud API drops the leading "9" from Argentinian mobile
// numbers in webhook `from` fields (e.g. "5492615562747" → "542615562747"),
// while user-facing formats and wamid payloads keep it. To make both forms
// match the same registry entry, we re-insert the "9" when missing.
func normalize(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	digits := b.String()
	if strings.HasPrefix(digits, "54") && !strings.HasPrefix(digits, "549") && len(digits) >= 12 {
		digits = "549" + digits[2:]
	}
	return digits
}
