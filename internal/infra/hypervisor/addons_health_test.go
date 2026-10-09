package hypervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func newCRDTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, kubernetes.Interface) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := &rest.Config{Host: srv.URL}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	return srv, cs
}

func crdWithLabel(name, labelValue string, established bool) string {
	cond := `{"type":"Established","status":"False"}`
	if established {
		cond = `{"type":"Established","status":"True"}`
	}
	return `{
		"metadata": {
			"name": "` + name + `",
			"labels": {"app.kubernetes.io/name": "` + labelValue + `"}
		},
		"status": {"conditions": [` + cond + `]}
	}`
}

func TestAddonsHealthDiscoversFromCRDs(t *testing.T) {
	t.Parallel()
	srv, cs := newCRDTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/apiextensions.k8s.io/v1/customresourcedefinitions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items": [
				` + crdWithLabel("virtualmachines.kubevirt.io", "kubevirt", true) + `,
				` + crdWithLabel("datavolumes.cdi.kubevirt.io", "cdi", true) + `,
				` + crdWithLabel("networkattachmentdefinitions.k8s.cni.cncf.io", "multus", true) + `,
				` + crdWithLabel("certificates.cert-manager.io", "cert-manager", true) + `,
				` + crdWithLabel("some.bare.crd", "", true) + `,
				` + crdWithLabel("foo.example.com", "failing-addon", false) + `
			]}`))
		default:
			http.NotFound(w, r)
		}
	})
	_ = srv

	d := &KubeVirtDriver{k8sClient: cs}
	got, err := d.AddonsHealth(context.Background())
	if err != nil {
		t.Fatalf("AddonsHealth: %v", err)
	}
	byName := map[string]string{}
	for _, a := range got.Addons {
		byName[a.Name] = a.Status
	}
	want := map[string]string{
		"kubevirt":     "ok",
		"cdi":          "ok",
		"multus":       "ok",
		"cert-manager": "ok",
		"failing-addon": "degraded",
	}
	for name, status := range want {
		if got := byName[name]; got != status {
			t.Fatalf("addon %q: got %q want %q", name, got, status)
		}
	}
	if _, present := byName[""]; present {
		t.Fatal("CRDs without app.kubernetes.io/name label must be ignored")
	}
	if got.CheckedAt.IsZero() {
		t.Fatal("checked_at must be set")
	}
}

func TestAddonsHealthCRDListForbidden(t *testing.T) {
	t.Parallel()
	srv, cs := newCRDTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"kind":"Status","status":"Failure","code":403,"reason":"Forbidden"}`, http.StatusForbidden)
	})
	_ = srv

	d := &KubeVirtDriver{k8sClient: cs}
	got, err := d.AddonsHealth(context.Background())
	if err != nil {
		t.Fatalf("AddonsHealth: %v", err)
	}
	if len(got.Addons) != 0 {
		t.Fatalf("forbidden CRD list must yield empty addons, got %+v", got.Addons)
	}
}

func TestAddonsHealthDedupMultipleCRDsSameName(t *testing.T) {
	t.Parallel()
	srv, cs := newCRDTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apis/apiextensions.k8s.io/v1/customresourcedefinitions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items": [
				` + crdWithLabel("virtualmachines.kubevirt.io", "kubevirt", true) + `,
				` + crdWithLabel("virtualmachineinstances.kubevirt.io", "kubevirt", true) + `
			]}`))
		default:
			http.NotFound(w, r)
		}
	})
	_ = srv

	d := &KubeVirtDriver{k8sClient: cs}
	got, err := d.AddonsHealth(context.Background())
	if err != nil {
		t.Fatalf("AddonsHealth: %v", err)
	}
	count := 0
	for _, a := range got.Addons {
		if a.Name == "kubevirt" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("multiple CRDs with same app.kubernetes.io/name must dedup, got %d entries", count)
	}
}
