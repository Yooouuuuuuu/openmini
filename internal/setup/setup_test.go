package setup

import (
	"strings"
	"testing"

	"openmini/internal/config"
)

func TestSetEnabledAndValue(t *testing.T) {
	body := config.Template
	body = setEnabled(body, "web", false)
	body = setEnabled(body, "agyapi", true)
	body = setValue(body, "server", "default_backend", "agyapi")
	for _, want := range []string{"[web]\n# Drives", "default_backend = \"agyapi\""} {
		if !strings.Contains(body, want) {
			t.Errorf("edited template lacks %q", want)
		}
	}
	// only the [web] enabled line changes
	i := strings.Index(body, "[web]")
	if j := strings.Index(body[i:], "enabled = "); !strings.HasPrefix(body[i+j:], "enabled = false") {
		t.Errorf("[web] enabled not set to false: %q", body[i+j:i+j+20])
	}
	if strings.Count(body, "enabled = false") != strings.Count(config.Template, "enabled = false")+1 {
		t.Error("setEnabled touched more than one section")
	}
}

func TestStorytellerAgent(t *testing.T) {
	// agy 1.4.7 drops an agent whose commandExecutionPolicy is "never"
	if strings.Contains(storytellerAgent, "commandExecutionPolicy: never") || !strings.Contains(storytellerAgent, "commandExecutionPolicy: off") {
		t.Error("storyteller agent must use commandExecutionPolicy: off")
	}
	if !strings.HasPrefix(storytellerAgent, "---\nname: storyteller\n") {
		t.Error("agent frontmatter must start with its name")
	}
}
