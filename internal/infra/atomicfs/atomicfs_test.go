package atomicfs

import (
	"os"
	"path/filepath"
	"testing"
)

type payload struct {
	Name string `json:"name"`
	N    int    `json:"n"`
}

// TestWriteJSON pins the exact byte layout (two-space MarshalIndent plus a
// trailing newline), the final file mode, and directory auto-creation.
func TestWriteJSON(t *testing.T) {
	tests := []struct {
		name     string
		relPath  string
		v        any
		perm     os.FileMode
		want     string
		wantPerm os.FileMode
		durable  bool
	}{
		{
			name:     "struct is indented with trailing newline",
			relPath:  "out.json",
			v:        payload{Name: "a", N: 7},
			perm:     0o644,
			want:     "{\n  \"name\": \"a\",\n  \"n\": 7\n}\n",
			wantPerm: 0o644,
		},
		{
			name:     "0600 perm is applied exactly",
			relPath:  "secret.json",
			v:        map[string]string{"token": "t"},
			perm:     0o600,
			want:     "{\n  \"token\": \"t\"\n}\n",
			wantPerm: 0o600,
		},
		{
			name:     "missing parent directories are created",
			relPath:  filepath.Join("a", "b", "out.json"),
			v:        payload{},
			perm:     0o644,
			want:     "{\n  \"name\": \"\",\n  \"n\": 0\n}\n",
			wantPerm: 0o644,
		},
		{
			name:     "durable replacement keeps exact bytes and mode",
			relPath:  "durable.json",
			v:        payload{Name: "durable", N: 1},
			perm:     0o600,
			want:     "{\n  \"name\": \"durable\",\n  \"n\": 1\n}\n",
			wantPerm: 0o600,
			durable:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.relPath)
			write := WriteJSON
			if tt.durable {
				write = WriteJSONDurable
			}
			if err := write(path, tt.v, tt.perm); err != nil {
				t.Fatalf("WriteJSON(%q) = %v, want nil", path, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile(%q) = %v, want nil", path, err)
			}
			if got := string(data); got != tt.want {
				t.Errorf("WriteJSON wrote %q, want %q", got, tt.want)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("Stat(%q) = %v, want nil", path, err)
			}
			if got := info.Mode().Perm(); got != tt.wantPerm {
				t.Errorf("WriteJSON perm = %v, want %v", got, tt.wantPerm)
			}
		})
	}
}

// TestWriteJSONUnmarshalableValue guarantees the marshal error surfaces and no
// file is left behind.
func TestWriteJSONUnmarshalableValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.json")
	if err := WriteJSON(path, func() {}, 0o644); err == nil {
		t.Fatal("WriteJSON(func) = nil, want marshal error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Stat(%q) after failed WriteJSON = %v, want IsNotExist", path, err)
	}
}

func TestWriteJSONDurableRequiresExistingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "out.json")
	if err := WriteJSONDurable(path, payload{}, 0o600); err == nil {
		t.Fatal("WriteJSONDurable() created an unverified destination directory")
	}
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("durable write created destination directory: %v", err)
	}
}

// TestReadJSON pins the (found, err) contract: missing file (false, nil),
// read failure (false, err), decode failure (true, err), success (true, nil).
func TestReadJSON(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T, dir string) string // returns path
		wantFound bool
		wantErr   bool
		want      payload
	}{
		{
			name: "missing file is found=false with nil error",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				return filepath.Join(dir, "absent.json")
			},
			wantFound: false,
			wantErr:   false,
		},
		{
			name: "valid file decodes into v",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				path := filepath.Join(dir, "ok.json")
				if err := os.WriteFile(path, []byte(`{"name":"a","n":7}`), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantFound: true,
			wantErr:   false,
			want:      payload{Name: "a", N: 7},
		},
		{
			name: "malformed JSON is found=true with error",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				path := filepath.Join(dir, "bad.json")
				if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantFound: true,
			wantErr:   true,
		},
		{
			// A directory exists but cannot be read as a file: the read stage
			// fails, so found must stay false (distinguishes wrap messages).
			name: "unreadable path is found=false with error",
			setup: func(t *testing.T, dir string) string {
				t.Helper()
				path := filepath.Join(dir, "isadir")
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
				return path
			},
			wantFound: false,
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t, t.TempDir())
			var got payload
			found, err := ReadJSON(path, &got)
			if found != tt.wantFound {
				t.Errorf("ReadJSON(%q) found = %t, want %t", path, found, tt.wantFound)
			}
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadJSON(%q) err = %v, wantErr %t", path, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("ReadJSON(%q) decoded %+v, want %+v", path, got, tt.want)
			}
		})
	}
}

func TestReadJSONStrictRejectsDuplicateFieldsAtEveryObjectLevel(t *testing.T) {
	type nestedPayload struct {
		Value int `json:"value"`
	}
	type strictPayload struct {
		Name   string          `json:"name"`
		Nested nestedPayload   `json:"nested"`
		Items  []nestedPayload `json:"items"`
	}

	tests := []struct {
		name string
		body string
	}{
		{
			name: "top level",
			body: `{"name":"first","name":"second","nested":{"value":1},"items":[]}`,
		},
		{
			name: "nested object",
			body: `{"name":"ok","nested":{"value":1,"value":2},"items":[]}`,
		},
		{
			name: "object inside array",
			body: `{"name":"ok","nested":{"value":1},"items":[{"value":1,"\u0076alue":2}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "duplicate.json")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			var got strictPayload
			found, err := ReadJSONStrict(path, &got)
			if !found || err == nil {
				t.Fatalf("ReadJSONStrict() = found:%t err:%v, want duplicate-field error", found, err)
			}
		})
	}
}

func TestReadJSONStrictAcceptsUniqueNestedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unique.json")
	if err := os.WriteFile(
		path,
		[]byte(`{"name":"ok","nested":{"value":1},"items":[{"value":2}]}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Name   string `json:"name"`
		Nested struct {
			Value int `json:"value"`
		} `json:"nested"`
		Items []struct {
			Value int `json:"value"`
		} `json:"items"`
	}
	found, err := ReadJSONStrict(path, &got)
	if !found || err != nil {
		t.Fatalf("ReadJSONStrict() = found:%t err:%v", found, err)
	}
	if got.Name != "ok" || got.Nested.Value != 1 || len(got.Items) != 1 || got.Items[0].Value != 2 {
		t.Fatalf("ReadJSONStrict() decoded %+v", got)
	}
}
