package telemetry

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Identity is who ran the sessions, as the tools report it live.
type Identity struct {
	Email string
	// UserID is Claude Code's anonymous user.id hash; it attributes sessions without an email.
	UserID       string
	AccountUUID  string
	Organization string
	CodexAccount string
	CodexEmail   string
}

// ReadIdentity reads the signed-in accounts from the Claude Code and Codex homes.
// Only identifiers are read; tokens are never kept.
func ReadIdentity(claudeHome, codexHome string) Identity {
	var id Identity
	if b, err := os.ReadFile(claudeConfigPath(claudeHome)); err == nil {
		var cfg struct {
			UserID       string `json:"userID"`
			OAuthAccount struct {
				EmailAddress     string `json:"emailAddress"`
				AccountUUID      string `json:"accountUuid"`
				OrganizationUUID string `json:"organizationUuid"`
			} `json:"oauthAccount"`
		}
		if json.Unmarshal(b, &cfg) == nil {
			id.Email = strings.ToLower(cfg.OAuthAccount.EmailAddress)
			id.UserID = cfg.UserID
			id.AccountUUID = cfg.OAuthAccount.AccountUUID
			id.Organization = cfg.OAuthAccount.OrganizationUUID
		}
	}
	if b, err := os.ReadFile(filepath.Join(codexHome, "auth.json")); err == nil {
		var auth struct {
			Tokens struct {
				IDToken   string `json:"id_token"`
				AccountID string `json:"account_id"`
			} `json:"tokens"`
		}
		if json.Unmarshal(b, &auth) == nil {
			id.CodexAccount = auth.Tokens.AccountID
			id.CodexEmail = jwtEmail(auth.Tokens.IDToken)
		}
	}
	return id
}

// claudeConfigPath is ~/.claude.json for the default home and <home>/.claude.json otherwise.
// Another home, such as $CLAUDE_CONFIG_DIR, keeps its own config even when its name is
// .claude: the config next to it may be another account's.
func claudeConfigPath(claudeHome string) string {
	inside := filepath.Join(claudeHome, ".claude.json")
	if fileExists(inside) {
		return inside
	}
	if home, ok := defaultClaudeHomeOwner(claudeHome); ok {
		return filepath.Join(home, ".claude.json")
	}
	return inside
}

// defaultClaudeHomeOwner returns the user's home directory when claudeHome is its .claude
// directory, however it is spelled: relative, through a symlink, or with a trailing slash.
func defaultClaudeHomeOwner(claudeHome string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	given, err := os.Stat(claudeHome)
	if err != nil {
		return "", false
	}
	def, err := os.Stat(filepath.Join(home, ".claude"))
	return home, err == nil && os.SameFile(given, def)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// jwtEmail reads the email claim of an unverified JWT payload.
func jwtEmail(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Email string `json:"email"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return strings.ToLower(claims.Email)
}

// WithEmail overrides the email of both tools.
func (id Identity) WithEmail(email string) Identity {
	id.Email, id.CodexEmail = strings.ToLower(email), strings.ToLower(email)
	return id
}

func (id Identity) claudeAttributes() map[string]any {
	attrs := map[string]any{}
	set := func(key, value string) {
		if value != "" {
			attrs[key] = value
		}
	}
	set("user.email", id.Email)
	set("user.id", id.UserID)
	set("user.account_uuid", id.AccountUUID)
	set("organization.id", id.Organization)
	return attrs
}

func (id Identity) codexAttributes() map[string]any {
	attrs := map[string]any{}
	if id.CodexEmail != "" {
		attrs["user.email"] = id.CodexEmail
	}
	if id.CodexAccount != "" {
		attrs["user.account_id"] = id.CodexAccount
	}
	return attrs
}
