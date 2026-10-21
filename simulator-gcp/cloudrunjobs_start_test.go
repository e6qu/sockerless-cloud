package main

import (
	"slices"
	"testing"
)

// A container starts after every container its dependsOn names, the declared
// order holding among containers free to start; a name outside the template,
// a self-reference and a cycle are refused.
func TestCloudRunContainerStartOrder(t *testing.T) {
	order, err := cloudRunContainerStartOrder([]Container{
		{Name: "main", DependsOn: []string{"proxy", "cache"}},
		{Name: "cache"},
		{Name: "proxy", DependsOn: []string{"cache"}},
		{Name: "logger"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{1, 2, 0, 3}; !slices.Equal(order, want) {
		t.Fatalf("start order = %v, want %v", order, want)
	}
	for _, refused := range [][]Container{
		{{Name: "main", DependsOn: []string{"absent"}}},
		{{Name: "main", DependsOn: []string{"main"}}},
		{{Name: "a", DependsOn: []string{"b"}}, {Name: "b", DependsOn: []string{"a"}}},
	} {
		if _, err := cloudRunContainerStartOrder(refused); err == nil {
			t.Fatalf("containers %+v were accepted", refused)
		}
	}
}

// A service's Knative projection carries its containers' dependsOn in the
// revision template's container-dependencies annotation, and reading the
// projection back restores them onto the containers.
func TestCloudRunServiceProjectionCarriesContainerDependencies(t *testing.T) {
	service := ServiceV2{
		Name: "projects/p/locations/us-central1/services/ordered",
		Template: &RevisionTemplate{
			Annotations: map[string]string{"keep": "me"},
			Containers: []Container{
				{Name: "ingress", Image: "a", DependsOn: []string{"sidecar"}},
				{Name: "sidecar", Image: "b"},
			},
		},
	}
	v1 := cloudRunV2ToV1(service, "p", "ordered")
	if got := v1.Spec.Template.Metadata.Annotations[cloudRunContainerDependenciesAnnotation]; got != `{"ingress":["sidecar"]}` {
		t.Fatalf("container-dependencies annotation = %q", got)
	}
	back := cloudRunV1ToV2(v1, "p", "us-central1")
	if deps := back.Template.Containers[0].DependsOn; !slices.Equal(deps, []string{"sidecar"}) {
		t.Fatalf("restored dependsOn = %v", deps)
	}
	if _, kept := back.Template.Annotations[cloudRunContainerDependenciesAnnotation]; kept || back.Template.Annotations["keep"] != "me" {
		t.Fatalf("restored annotations = %v", back.Template.Annotations)
	}
}

// A Knative service's containerConcurrency and its template's minScale and
// maxScale annotations become the v2 template's request concurrency and
// scaling, and the v2 template folds back to the same Knative fields.
func TestCloudRunServiceProjectionCarriesScaling(t *testing.T) {
	v1 := CRService{
		Metadata: CRServiceMetadata{Name: "scaled"},
		Spec: CRServiceSpec{Template: &CRServiceTemplate{
			Metadata: &CRServiceMetadata{Annotations: map[string]string{
				cloudRunMinScaleAnnotation: "1", cloudRunMaxScaleAnnotation: "4", "keep": "me",
			}},
			Spec: &CRTemplateSpec{ContainerConcurrency: 3, Containers: []CRContainer{{Image: "a"}}},
		}},
	}
	v2 := cloudRunV1ToV2(v1, "p", "us-central1")
	if got := v2.Template.MaxInstanceRequestConcurrency; got != 3 {
		t.Fatalf("maxInstanceRequestConcurrency = %d, want 3", got)
	}
	if s := v2.Template.Scaling; s == nil || s.MinInstanceCount != 1 || s.MaxInstanceCount != 4 {
		t.Fatalf("scaling = %+v, want min 1 max 4", s)
	}
	if _, kept := v2.Template.Annotations[cloudRunMaxScaleAnnotation]; kept || v2.Template.Annotations["keep"] != "me" {
		t.Fatalf("v2 annotations = %v", v2.Template.Annotations)
	}
	back := cloudRunV2ToV1(v2, "p", "scaled")
	if back.Spec.Template.Spec.ContainerConcurrency != 3 ||
		back.Spec.Template.Metadata.Annotations[cloudRunMinScaleAnnotation] != "1" ||
		back.Spec.Template.Metadata.Annotations[cloudRunMaxScaleAnnotation] != "4" {
		t.Fatalf("folded back = %+v", back.Spec.Template)
	}
	if _, err := cloudRunUnfoldScaling(map[string]string{cloudRunMaxScaleAnnotation: "many"}, &RevisionTemplate{}); err == nil {
		t.Fatal("a maxScale that is not an integer was accepted")
	}
}
