package pkgmgr

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writePkg writes a package.json (and optional extra files) into a fresh temp
// dir and returns the dir. extraFiles maps a relative path to its contents; a
// path ending in "/" is created as a directory.
func writePkg(t *testing.T, packageJSON string, extraFiles map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(packageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, body := range extraFiles {
		p := filepath.Join(dir, name)
		if body == "" && name[len(name)-1] == '/' {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestClassifyExplicitType verifies a single explicit pi.type is recognized.
func TestClassifyExplicitType(t *testing.T) {
	dir := writePkg(t, `{"name":"pi-web","version":"1.0.0","pi":{"type":"skill"}}`, nil)
	name, version, types, err := Classify(dir)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if name != "pi-web" || version != "1.0.0" {
		t.Errorf("name/version = %q/%q, want pi-web/1.0.0", name, version)
	}
	if !reflect.DeepEqual(types, []PackageType{TypeSkill}) {
		t.Errorf("types = %v, want [skill]", types)
	}
}

// TestClassifyMultiType verifies a package declaring several types via pi.types
// returns them all, sorted.
func TestClassifyMultiType(t *testing.T) {
	dir := writePkg(t, `{"name":"combo","version":"2.0.0","pi":{"types":["extension","skill"]}}`, nil)
	_, _, types, err := Classify(dir)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := []PackageType{TypeExtension, TypeSkill}
	if !reflect.DeepEqual(types, want) {
		t.Errorf("types = %v, want %v", types, want)
	}
}

// TestClassifyPerCapabilityKeys verifies pi.extension + pi.theme blocks both
// register their types.
func TestClassifyPerCapabilityKeys(t *testing.T) {
	dir := writePkg(t, `{"name":"x","version":"0.1.0","pi":{"extension":{"cmd":"x"},"theme":{}}}`, nil)
	_, _, types, err := Classify(dir)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := []PackageType{TypeExtension, TypeTheme}
	if !reflect.DeepEqual(types, want) {
		t.Errorf("types = %v, want %v", types, want)
	}
}

// TestClassifyPluralCapabilityKeys verifies the pi-ecosystem convention of
// plural path arrays (pi.extensions, pi.skills, ...) registers each type. This
// is the shape published packages actually use (pi-simplify, pi-ask-user, ...).
func TestClassifyPluralCapabilityKeys(t *testing.T) {
	dir := writePkg(t, `{"name":"pi-ask-user","version":"1.0.0","pi":{"extensions":["./index.ts"],"skills":["./skills"]}}`, nil)
	_, _, types, err := Classify(dir)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := []PackageType{TypeExtension, TypeSkill}
	if !reflect.DeepEqual(types, want) {
		t.Errorf("types = %v, want %v", types, want)
	}
}

// TestClassifyPluralExtensionsOnly verifies a pure extension declared via
// pi.extensions (no bin) classifies as an extension — the pi-simplify case.
func TestClassifyPluralExtensionsOnly(t *testing.T) {
	dir := writePkg(t, `{"name":"pi-simplify","version":"0.2.3","pi":{"extensions":["dist/index.js"]}}`, nil)
	_, _, types, err := Classify(dir)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !reflect.DeepEqual(types, []PackageType{TypeExtension}) {
		t.Errorf("types = %v, want [extension]", types)
	}
}

// TestClassifyStructuralBin verifies a bare package with a bin entry is an
// extension even without pi metadata.
func TestClassifyStructuralBin(t *testing.T) {
	dir := writePkg(t, `{"name":"pi-mcp-adapter","version":"1.0.0","bin":{"pi-mcp-adapter":"./index.js"}}`, nil)
	_, _, types, err := Classify(dir)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !reflect.DeepEqual(types, []PackageType{TypeExtension}) {
		t.Errorf("types = %v, want [extension]", types)
	}
}

// TestClassifyStructuralSkillMd verifies a SKILL.md file signals a skill.
func TestClassifyStructuralSkillMd(t *testing.T) {
	dir := writePkg(t, `{"name":"pi-skill","version":"1.0.0"}`, map[string]string{
		"SKILL.md": "# a skill",
	})
	_, _, types, err := Classify(dir)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !reflect.DeepEqual(types, []PackageType{TypeSkill}) {
		t.Errorf("types = %v, want [skill]", types)
	}
}

// TestClassifyStructuralCommandsDir verifies a commands/ dir signals a prompt.
func TestClassifyStructuralCommandsDir(t *testing.T) {
	dir := writePkg(t, `{"name":"pi-cmds","version":"1.0.0"}`, map[string]string{
		"commands/hello.md": "hi",
	})
	_, _, types, err := Classify(dir)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !reflect.DeepEqual(types, []PackageType{TypePrompt}) {
		t.Errorf("types = %v, want [prompt]", types)
	}
}

// TestClassifyExplicitAndStructural verifies explicit metadata and structural
// signals union together (skill via SKILL.md, extension via pi.type).
func TestClassifyExplicitAndStructural(t *testing.T) {
	dir := writePkg(t, `{"name":"both","version":"1.0.0","pi":{"type":"extension"}}`, map[string]string{
		"SKILL.md": "# skill",
	})
	_, _, types, err := Classify(dir)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := []PackageType{TypeExtension, TypeSkill}
	if !reflect.DeepEqual(types, want) {
		t.Errorf("types = %v, want %v", types, want)
	}
}

// TestClassifyUnknown verifies a plain npm package with no pi signals errors.
func TestClassifyUnknown(t *testing.T) {
	dir := writePkg(t, `{"name":"lodash","version":"4.17.21"}`, nil)
	_, _, _, err := Classify(dir)
	if err == nil {
		t.Fatal("Classify of non-pi package = nil error, want error")
	}
	if !contains(err.Error(), "unrecognized pi package") {
		t.Errorf("error = %q, want to mention 'unrecognized pi package'", err)
	}
}

// TestClassifyMissingPackageJSON verifies a missing package.json errors.
func TestClassifyMissingPackageJSON(t *testing.T) {
	if _, _, _, err := Classify(t.TempDir()); err == nil {
		t.Fatal("Classify with no package.json = nil error, want error")
	}
}

// TestClassifyCorruptPackageJSON verifies malformed JSON errors clearly.
func TestClassifyCorruptPackageJSON(t *testing.T) {
	dir := writePkg(t, `{not valid json`, nil)
	_, _, _, err := Classify(dir)
	if err == nil {
		t.Fatal("Classify with corrupt package.json = nil error, want error")
	}
	if !contains(err.Error(), "parse package.json") {
		t.Errorf("error = %q, want to mention 'parse package.json'", err)
	}
}

// TestClassifyUnknownTypeStringIgnored verifies an unrecognized pi.type string
// is ignored (not fatal) but leaves the package unclassified if nothing else
// matches.
func TestClassifyUnknownTypeStringIgnored(t *testing.T) {
	dir := writePkg(t, `{"name":"x","version":"1.0.0","pi":{"type":"widget"}}`, nil)
	_, _, _, err := Classify(dir)
	if err == nil {
		t.Fatal("Classify with only unknown pi.type = nil error, want error")
	}
}
