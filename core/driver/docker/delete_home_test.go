// SPDX-License-Identifier: Apache-2.0

package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/portenv/portenv/core/driver"
)

// volumeEngine is a fake engine with containers and labelled volumes: it
// answers inspects and removals and records every removal (ADR 0029).
type volumeEngine struct {
	mu         sync.Mutex
	containers map[string]bool
	volumes    map[string]string // name → its dev.portenv.box label
	removed    []string          // "container NAME" or "volume NAME", in order
}

func (f *volumeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path
	name := p[strings.LastIndex(p, "/")+1:]
	switch {
	case strings.Contains(p, "/containers/") && strings.HasSuffix(p, "/json"):
		n := strings.TrimSuffix(p[strings.Index(p, "/containers/")+len("/containers/"):], "/json")
		if !f.containers[n] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such container"}`))
			return
		}
		_, _ = w.Write([]byte(`{"Id":"x","Name":"/` + n + `","State":{"Running":false}}`))
	case strings.Contains(p, "/containers/") && r.Method == http.MethodDelete:
		if !f.containers[name] {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such container"}`))
			return
		}
		delete(f.containers, name)
		f.removed = append(f.removed, "container "+name)
		w.WriteHeader(http.StatusNoContent)
	case strings.Contains(p, "/volumes/") && r.Method == http.MethodGet:
		label, ok := f.volumes[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such volume"}`))
			return
		}
		_, _ = w.Write([]byte(`{"Name":"` + name + `","Labels":{"dev.portenv.box":"` + label + `"}}`))
	case strings.Contains(p, "/volumes/") && r.Method == http.MethodDelete:
		if _, ok := f.volumes[name]; !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"No such volume"}`))
			return
		}
		delete(f.volumes, name)
		f.removed = append(f.removed, "volume "+name)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not in this test"}`))
	}
}

func (f *volumeEngine) gone() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.removed)
}

func volumeDriver(t *testing.T, f *volumeEngine, homesDir string) *Driver {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	d := driverAt(t, "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	d.cfg.HomesDir = homesDir
	return d
}

// TestDeleteHomeRemovesOnlyThisBoxsStorage (ADR 0029): DeleteHome removes
// the box's home volume and its restic cache volume, both labelled with its
// ID; a second call finds nothing and succeeds; another box's volumes stay.
func TestDeleteHomeRemovesOnlyThisBoxsStorage(t *testing.T) {
	const id, other = "box-0123456789ab", "box-ffffffffffff"
	f := &volumeEngine{containers: map[string]bool{"portenv-" + other: true}, volumes: map[string]string{
		"portenv-home-" + id: id, "portenv-cache-" + id: id,
		"portenv-home-" + other: other, "portenv-cache-" + other: other,
	}}
	d := volumeDriver(t, f, "")
	home := driver.HomeStorage{Ref: "portenv-home-" + id}
	for range 2 {
		if err := d.DeleteHome(context.Background(), id, home); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := f.gone(), []string{"volume portenv-home-" + id, "volume portenv-cache-" + id}; !slices.Equal(got, want) {
		t.Fatalf("removed %v, want %v", got, want)
	}
	for _, v := range []string{"portenv-home-" + other, "portenv-cache-" + other} {
		if _, ok := f.volumes[v]; !ok {
			t.Fatalf("another workspace's volume %s was removed", v)
		}
	}
}

// TestDeleteHomeRefusesAnotherBoxsVolume: a home reference naming a volume
// labelled for another box is refused, and nothing is removed.
func TestDeleteHomeRefusesAnotherBoxsVolume(t *testing.T) {
	const id, other = "box-0123456789ab", "box-ffffffffffff"
	f := &volumeEngine{containers: map[string]bool{}, volumes: map[string]string{"portenv-home-" + other: other}}
	d := volumeDriver(t, f, "")
	if err := d.DeleteHome(context.Background(), id, driver.HomeStorage{Ref: "portenv-home-" + other}); err == nil {
		t.Fatal("deleted another workspace's home")
	}
	if got := f.gone(); len(got) != 0 {
		t.Fatalf("removed %v", got)
	}
}

// TestDeleteHomeRefusesWhileTheInstanceExists: the instance goes first
// (Destroy); DeleteHome never pulls storage from under a box.
func TestDeleteHomeRefusesWhileTheInstanceExists(t *testing.T) {
	const id = "box-0123456789ab"
	f := &volumeEngine{containers: map[string]bool{"portenv-" + id: true}, volumes: map[string]string{"portenv-home-" + id: id}}
	d := volumeDriver(t, f, "")
	if err := d.DeleteHome(context.Background(), id, driver.HomeStorage{Ref: "portenv-home-" + id}); err == nil {
		t.Fatal("deleted the home of a box whose instance exists")
	}
	if got := f.gone(); len(got) != 0 {
		t.Fatalf("removed %v", got)
	}
	// Destroy, then DeleteHome: instance first, storage second.
	if err := d.Destroy(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteHome(context.Background(), id, driver.HomeStorage{Ref: "portenv-home-" + id}); err != nil {
		t.Fatal(err)
	}
	if got, want := f.gone(), []string{"container portenv-" + id, "volume portenv-home-" + id}; !slices.Equal(got, want) {
		t.Fatalf("removed %v, want %v", got, want)
	}
}

// TestDeleteHomeRemovesItsFolderInHomesDir: on a server, a home volume is a
// bind of HomesDir/<volume>; that folder goes too, and no other.
func TestDeleteHomeRemovesItsFolderInHomesDir(t *testing.T) {
	const id, other = "box-0123456789ab", "box-ffffffffffff"
	homes := t.TempDir()
	for _, v := range []string{"portenv-home-" + id, "portenv-home-" + other} {
		if err := os.MkdirAll(filepath.Join(homes, v, "work"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f := &volumeEngine{containers: map[string]bool{}, volumes: map[string]string{"portenv-home-" + id: id}}
	d := volumeDriver(t, f, homes)
	if err := d.DeleteHome(context.Background(), id, driver.HomeStorage{Ref: "portenv-home-" + id}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(homes, "portenv-home-"+id)); !os.IsNotExist(err) {
		t.Fatalf("its folder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(homes, "portenv-home-"+other, "work")); err != nil {
		t.Fatalf("another workspace's folder: %v", err)
	}
}

// TestDeleteHomeRemovesTheUnlabelledCacheVolume: Docker makes the restic
// cache volume from the mount itself, with no labels (found by the
// conformance suite on CI): its name comes from the box's ID, so it goes
// without a label; one labelled for another box is still refused. The home
// volume always needs this box's label.
func TestDeleteHomeRemovesTheUnlabelledCacheVolume(t *testing.T) {
	const id = "box-0123456789ab"
	f := &volumeEngine{containers: map[string]bool{}, volumes: map[string]string{"portenv-home-" + id: id, "portenv-cache-" + id: ""}}
	d := volumeDriver(t, f, "")
	if err := d.DeleteHome(context.Background(), id, driver.HomeStorage{Ref: "portenv-home-" + id}); err != nil {
		t.Fatal(err)
	}
	if got, want := f.gone(), []string{"volume portenv-home-" + id, "volume portenv-cache-" + id}; !slices.Equal(got, want) {
		t.Fatalf("removed %v, want %v", got, want)
	}

	f = &volumeEngine{containers: map[string]bool{}, volumes: map[string]string{"portenv-home-" + id: ""}}
	d = volumeDriver(t, f, "")
	if err := d.DeleteHome(context.Background(), id, driver.HomeStorage{Ref: "portenv-home-" + id}); err == nil || len(f.gone()) != 0 {
		t.Fatalf("an unlabelled home volume: %v, removed %v", err, f.gone())
	}
	f = &volumeEngine{containers: map[string]bool{}, volumes: map[string]string{"portenv-cache-" + id: "box-ffffffffffff"}}
	d = volumeDriver(t, f, "")
	if err := d.DeleteHome(context.Background(), id, driver.HomeStorage{Ref: "portenv-home-" + id}); err == nil || len(f.gone()) != 0 {
		t.Fatalf("a cache volume labelled for another box: %v, removed %v", err, f.gone())
	}
}
