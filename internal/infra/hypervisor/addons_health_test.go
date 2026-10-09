package hypervisor

import (
	"context"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes/fake"
)

func TestAddonsHealthNilClient(t *testing.T) {
	t.Parallel()
	d := &KubeVirtDriver{}
	got, err := d.AddonsHealth(context.Background())
	if err != nil {
		t.Fatalf("nil client: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result so the UI can render a neutral strip")
	}
	if len(got.Addons) == 0 {
		t.Fatal("expected the static addon list to be reported even without a client")
	}
	for _, a := range got.Addons {
		if a.Status != "unknown" {
			t.Fatalf("nil client must mark every addon unknown, got %s=%s", a.Name, a.Status)
		}
	}
}

func TestAddonsHealthFakeClientAllReported(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	d := &KubeVirtDriver{k8sClient: cs}
	got, err := d.AddonsHealth(context.Background())
	if err != nil {
		t.Fatalf("AddonsHealth: %v", err)
	}
	names := map[string]string{}
	for _, a := range got.Addons {
		names[a.Name] = a.Status
	}
	for _, want := range []string{"kubevirt", "cdi", "multus", "metrics-server", "networking"} {
		if _, ok := names[want]; !ok {
			t.Fatalf("addon %q missing from probe list", want)
		}
	}
	if got.CheckedAt.IsZero() {
		t.Fatal("checked_at must be set")
	}
}

func TestAddonsHealthCache(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	d := &KubeVirtDriver{k8sClient: cs}
	if _, err := d.AddonsHealth(context.Background()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	d.addonsCacheMu.Lock()
	cached := d.addonsCache
	d.addonsCacheMu.Unlock()
	if cached == nil {
		t.Fatal("cache must be populated after first call")
	}
	second, err := d.AddonsHealth(context.Background())
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if second != cached.value {
		t.Fatal("second call should hit the cache and return the same pointer")
	}
}

func TestAddonsHealthCacheTTLExpiry(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset()
	d := &KubeVirtDriver{k8sClient: cs}
	if _, err := d.AddonsHealth(context.Background()); err != nil {
		t.Fatalf("first call: %v", err)
	}
	firstChecked := d.addonsCache.value.CheckedAt
	d.addonsCacheMu.Lock()
	d.addonsCache.at = time.Now().Add(-2 * clusterUsageTTL)
	d.addonsCacheMu.Unlock()
	time.Sleep(2 * time.Millisecond)
	second, err := d.AddonsHealth(context.Background())
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if !second.CheckedAt.After(firstChecked) {
		t.Fatalf("cache TTL did not expire: second.CheckedAt=%v first=%v", second.CheckedAt, firstChecked)
	}
}

func TestClassifyAddonNil(t *testing.T) {
	t.Parallel()
	if got := classifyAddon("x", nil).Status; got != "ok" {
		t.Fatalf("nil error must classify as ok, got %q", got)
	}
}
