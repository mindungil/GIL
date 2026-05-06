package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRoot_HasAdvancedGroup(t *testing.T) {
	root := Root()
	var found bool
	for _, g := range root.Groups() {
		if g.ID == "advanced" {
			found = true
		}
	}
	require.True(t, found, "expected 'advanced' cobra group registered")
}

func TestRoot_VerbsHaveGroupID(t *testing.T) {
	root := Root()
	expectAdvanced := []string{
		"run", "watch", "events",
		"stats", "import", "export",
	}
	advancedSet := map[string]bool{}
	for _, c := range root.Commands() {
		if c.GroupID == "advanced" {
			// c.Use can be "interview <id>" or just "interview" — match the
			// first whitespace-bounded word.
			name := strings.SplitN(c.Use, " ", 2)[0]
			advancedSet[name] = true
		}
	}
	for _, name := range expectAdvanced {
		require.True(t, advancedSet[name], "verb %q should be GroupID=advanced", name)
	}
}

// Sanity: the conversational surface (chat) and quick-look (status) must
// NOT be moved to advanced — they remain in the primary groups.
func TestRoot_PrimaryVerbsNotInAdvanced(t *testing.T) {
	root := Root()
	keepPrimary := []string{"chat", "status", "init", "auth", "doctor"}
	for _, c := range root.Commands() {
		name := strings.SplitN(c.Use, " ", 2)[0]
		for _, k := range keepPrimary {
			if name == k {
				require.NotEqual(t, "advanced", c.GroupID,
					"%q must not be moved to advanced", name)
			}
		}
	}
}
