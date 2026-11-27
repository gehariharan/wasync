package plugins

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestMarketplaceMatchesRepository(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", ".agents", "plugins", "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want, got any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(Marketplace), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("Marketplace differs from .agents/plugins/marketplace.json:\n%s", Marketplace)
	}
}

func TestEmbedsEveryPluginFile(t *testing.T) {
	var onDisk, embedded []string
	if err := filepath.WalkDir("whatsapp", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			onDisk = append(onDisk, filepath.ToSlash(p))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := fs.WalkDir(WhatsApp, "whatsapp", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			embedded = append(embedded, p)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(onDisk)
	sort.Strings(embedded)
	if !reflect.DeepEqual(onDisk, embedded) || len(embedded) == 0 {
		t.Fatalf("embedded %v, on disk %v", embedded, onDisk)
	}
}
