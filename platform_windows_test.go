package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

func init() {
	isolatePlatform = func(dir string) func() {
		os.Setenv("APPDATA", dir)
		root := `Software\NavigaraTest\` + strconv.Itoa(os.Getpid())
		userEnvKey, envOwnerKey = root+`\Environment`, root+`\Owner`
		machineEnvKey.root, machineEnvKey.path = registry.CURRENT_USER, root+`\Machine`
		k, _, err := registry.CreateKey(registry.CURRENT_USER, userEnvKey, registry.ALL_ACCESS)
		if err != nil {
			panic(err)
		}
		k.Close()
		return func() {
			for _, key := range []string{userEnvKey, envOwnerKey, machineEnvKey.path, root, `Software\NavigaraTest`} {
				_ = registry.DeleteKey(registry.CURRENT_USER, key)
			}
		}
	}
}

// cleanUserEnv deletes the test variable and its records.
func cleanUserEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"", previousSuffix, createdSuffix} {
		if err := setEnvOwner("OTEL_RESOURCE_ATTRIBUTES"+name, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := setUserEnv("OTEL_RESOURCE_ATTRIBUTES", ""); err != nil {
		t.Fatal(err)
	}
}

func TestCodexEmailInUserEnvironment(t *testing.T) {
	cleanUserEnv(t)
	if err := setUserEnv("OTEL_RESOURCE_ATTRIBUTES", "team=core,enduser.id=old@example.com"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	codex := filepath.Join(home, ".codex")
	if err := os.Mkdir(codex, 0700); err != nil {
		t.Fatal(err)
	}
	opts := options{source: "codex", codexHome: codex, url: "https://app.example/api", token: "secret", email: "dev@example.com"}
	files, err := setupPlans(opts, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[1].path != `HKCU\Environment\OTEL_RESOURCE_ATTRIBUTES` {
		t.Fatalf("plans: %+v", files)
	}
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := getUserEnv("OTEL_RESOURCE_ATTRIBUTES"); got != "team=core,enduser.id=dev@example.com" {
		t.Fatalf("after setup = %q", got)
	}
	if email, err := codexEmail(""); err != nil || email != "dev@example.com" {
		t.Fatalf("codexEmail = %q, %v", email, err)
	}
	if files, err = setupPlans(opts, home); err != nil || !files[1].unchanged() {
		t.Fatalf("second setup changes the variable: %+v, %v", files, err)
	}

	files, err = removePlans(opts, home, []string{"https://app.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := getUserEnv("OTEL_RESOURCE_ATTRIBUTES"); got != "team=core,enduser.id=old@example.com" {
		t.Fatalf("after remove = %q", got)
	}
	if _, err := codexEmail(""); err == nil {
		t.Fatal("codexEmail found an email after remove")
	}
}

func TestSetupRejectsUserEnvironmentChangedDuringSetup(t *testing.T) {
	cleanUserEnv(t)
	if err := setUserEnv("OTEL_RESOURCE_ATTRIBUTES", ""); err != nil {
		t.Fatal(err)
	}
	f, err := planEnv("OTEL_RESOURCE_ATTRIBUTES", func(string) (string, error) { return "enduser.id=dev@example.com", nil })
	if err != nil || f.existed {
		t.Fatalf("plan = %+v, %v", f, err)
	}
	if err := setUserEnv("OTEL_RESOURCE_ATTRIBUTES", "enduser.id=other@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles([]*setupFile{f}, func() error { return nil }); err == nil {
		t.Fatal("setup replaced a value that changed after the plan")
	}
	if got, _, _ := getUserEnv("OTEL_RESOURCE_ATTRIBUTES"); got != "enduser.id=other@example.com" {
		t.Fatalf("value = %q", got)
	}
}

func TestUserEnvironmentKeepsForeignValues(t *testing.T) {
	cleanUserEnv(t)
	k, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	err = k.SetExpandStringValue("OTEL_RESOURCE_ATTRIBUTES", "flag,host.name=%COMPUTERNAME%")
	k.Close()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	opts := options{source: "codex", codexHome: home, url: "https://app.example/api", token: "secret", email: "dev@example.com"}
	files, err := setupPlans(opts, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	k, err = registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	if got, kind, err := k.GetStringValue("OTEL_RESOURCE_ATTRIBUTES"); err != nil || kind != registry.EXPAND_SZ || got != "flag,host.name=%COMPUTERNAME%,enduser.id=dev@example.com" {
		t.Fatalf("value = %q, kind %d, %v", got, kind, err)
	}

	if err := setUserEnv("OTEL_RESOURCE_ATTRIBUTES", "a=1, b=2"); err != nil {
		t.Fatal(err)
	}
	files, err = removePlans(opts, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.env != "" && !f.unchanged() {
			t.Fatalf("remove changes a value without enduser.id: %q", f.after)
		}
	}
}

func TestClaudeManagedSettingsOnWindows(t *testing.T) {
	t.Setenv("ProgramW6432", "")
	t.Setenv("ProgramFiles", `C:\Program Files`)
	if got := claudeManagedSettings(); got != `C:\Program Files\ClaudeCode\managed-settings.json` {
		t.Fatalf("managed settings = %q", got)
	}
}

func TestRemoveKeepsEmailWithoutNavigaraCodex(t *testing.T) {
	cleanUserEnv(t)
	if err := setUserEnv("OTEL_RESOURCE_ATTRIBUTES", "enduser.id=me@corp.example"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"o3\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := removePlans(options{source: "codex", codexHome: home}, home, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.env != "" {
			t.Fatalf("remove plans the user's own email: %q", f.after)
		}
	}
}

func TestRemoveKeepsEmailChangedAfterSetup(t *testing.T) {
	cleanUserEnv(t)
	if err := setUserEnv("OTEL_RESOURCE_ATTRIBUTES", ""); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	opts := options{source: "codex", codexHome: home, url: "https://app.example/api", token: "secret", email: "dev@example.com"}
	files, err := setupPlans(opts, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := setUserEnv("OTEL_RESOURCE_ATTRIBUTES", "team=core,enduser.id=their-id"); err != nil {
		t.Fatal(err)
	}
	if _, err := codexEmail(""); err == nil {
		t.Fatal("status accepted an email that setup did not write")
	}
	files, err = removePlans(opts, home, []string{"https://app.example"})
	if err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := getUserEnv("OTEL_RESOURCE_ATTRIBUTES"); got != "team=core,enduser.id=their-id" {
		t.Fatalf("remove changed the user's email: %q", got)
	}
}

func TestRemoveRestoresEmailThatSetupReplaced(t *testing.T) {
	cleanUserEnv(t)
	if err := setUserEnv("OTEL_RESOURCE_ATTRIBUTES", "team=core,enduser.id=me@corp.example"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	opts := options{source: "codex", codexHome: home, url: "https://app.example/api", token: "secret", email: "dev@example.com"}
	files, err := setupPlans(opts, home)
	if err != nil {
		t.Fatal(err)
	}
	if notes := strings.Join(files[1].notes, "\n"); !strings.Contains(notes, "Replaces your enduser.id=me@corp.example") {
		t.Fatalf("notes = %q", notes)
	}
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := getUserEnv("OTEL_RESOURCE_ATTRIBUTES"); got != "team=core,enduser.id=dev@example.com" {
		t.Fatalf("after setup = %q", got)
	}
	if files, err = removePlans(opts, home, []string{"https://app.example"}); err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := getUserEnv("OTEL_RESOURCE_ATTRIBUTES"); got != "team=core,enduser.id=me@corp.example" {
		t.Fatalf("after remove = %q", got)
	}
	if getEnvOwner("OTEL_RESOURCE_ATTRIBUTES") != "" || getEnvOwner("OTEL_RESOURCE_ATTRIBUTES"+previousSuffix) != "" {
		t.Fatal("remove kept the records")
	}
}

func TestRemoveShowsMachineValueAgain(t *testing.T) {
	cleanUserEnv(t)
	if err := setUserEnv("OTEL_RESOURCE_ATTRIBUTES", ""); err != nil {
		t.Fatal(err)
	}
	k, _, err := registry.CreateKey(machineEnvKey.root, machineEnvKey.path, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	err = k.SetExpandStringValue("OTEL_RESOURCE_ATTRIBUTES", "host.name=%COMPUTERNAME%,enduser.id=machine@corp.example")
	k.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = registry.DeleteKey(machineEnvKey.root, machineEnvKey.path) })
	home := t.TempDir()
	opts := options{source: "codex", codexHome: home, url: "https://app.example/api", token: "secret", email: "dev@example.com"}
	files, err := setupPlans(opts, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	u, err := registry.OpenKey(registry.CURRENT_USER, userEnvKey, registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	got, kind, err := u.GetStringValue("OTEL_RESOURCE_ATTRIBUTES")
	u.Close()
	if err != nil || kind != registry.EXPAND_SZ || got != "host.name=%COMPUTERNAME%,enduser.id=dev@example.com" {
		t.Fatalf("after setup = %q, kind %d, %v", got, kind, err)
	}
	if files, err = removePlans(opts, home, []string{"https://app.example"}); err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, exists, _ := getUserEnv("OTEL_RESOURCE_ATTRIBUTES"); exists {
		t.Fatal("remove kept the user variable that hides the machine value")
	}
}

func TestRemoveKeepsAttributesAddedAfterSetup(t *testing.T) {
	cleanUserEnv(t)
	home := t.TempDir()
	opts := options{source: "codex", codexHome: home, url: "https://app.example/api", token: "secret", email: "dev@example.com"}
	files, err := setupPlans(opts, home)
	if err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := setUserEnv("OTEL_RESOURCE_ATTRIBUTES", "service.name=x,enduser.id=dev@example.com"); err != nil {
		t.Fatal(err)
	}
	if files, err = removePlans(opts, home, []string{"https://app.example"}); err != nil {
		t.Fatal(err)
	}
	if err := applySetupFiles(files, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := getUserEnv("OTEL_RESOURCE_ATTRIBUTES"); got != "service.name=x" {
		t.Fatalf("after remove = %q", got)
	}
}
