package config

import "testing"

func TestParseValid(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []HiveConfig
	}{
		{
			"single hive",
			[]string{"Machine=/var/lib/registry/machine.regdb"},
			[]HiveConfig{{Name: "Machine", Path: "/var/lib/registry/machine.regdb"}},
		},
		{
			"multiple hives",
			[]string{
				"Machine=/var/lib/registry/machine.regdb",
				"Users=/var/lib/registry/users.regdb",
			},
			[]HiveConfig{
				{Name: "Machine", Path: "/var/lib/registry/machine.regdb"},
				{Name: "Users", Path: "/var/lib/registry/users.regdb"},
			},
		},
		{
			"three hives",
			[]string{
				"Machine=/a.db",
				"Users=/b.db",
				"Roles=/c.db",
			},
			[]HiveConfig{
				{Name: "Machine", Path: "/a.db"},
				{Name: "Users", Path: "/b.db"},
				{Name: "Roles", Path: "/c.db"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.args)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d configs, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].Name != tt.want[i].Name || got[i].Path != tt.want[i].Path {
					t.Errorf("config[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"no arguments", nil},
		{"empty slice", []string{}},
		{"missing equals", []string{"Machine/var/lib/machine.regdb"}},
		{"empty hive name", []string{"=/var/lib/machine.regdb"}},
		{"empty path", []string{"Machine="}},
		{"relative path", []string{"Machine=relative/path.db"}},
		{"backslash in name", []string{"Mach\\ine=/a.db"}},
		{"forward slash in name", []string{"Mach/ine=/a.db"}},
		{"null in name", []string{"Mach\x00ine=/a.db"}},
		{"CurrentUser reserved", []string{"CurrentUser=/a.db"}},
		{"currentuser reserved", []string{"currentuser=/a.db"}},
		{"CURRENTUSER reserved", []string{"CURRENTUSER=/a.db"}},
		{"duplicate hive name", []string{"Machine=/a.db", "Machine=/b.db"}},
		{"duplicate case insensitive", []string{"Machine=/a.db", "machine=/b.db"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.args)
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestParseCasePreservation(t *testing.T) {
	got, err := Parse([]string{"MyHive=/a.db"})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Name != "MyHive" {
		t.Errorf("name = %q, want %q (case should be preserved)", got[0].Name, "MyHive")
	}
}
