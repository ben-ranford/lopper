package shared

import (
	"encoding/json"
	"encoding/xml"
	"reflect"
	"sync"
	"testing"
)

func TestBuildPomPropertyMapUsesParentFallbacksAndIgnoresBlankValues(t *testing.T) {
	propertyMap := inventoryPOMProperties(pomProjectModel{
		ArtifactID: "demo-artifact",
		Parent: pomParentModel{
			GroupID: "com.example.parent",
			Version: "1.2.3",
		},
		Properties: pomPropertiesModel{
			Properties: []pomPropertyModel{
				{XMLName: xml.Name{Local: "ok"}, Value: " value "},
				{XMLName: xml.Name{Local: ""}, Value: "ignored"},
				{XMLName: xml.Name{Local: "blankValue"}, Value: " "},
			},
		},
	})
	if propertyMap["ok"] != "value" {
		t.Fatalf("expected trimmed explicit property, got %#v", propertyMap)
	}
	if propertyMap["project.groupId"] != "com.example.parent" || propertyMap["project.version"] != "1.2.3" {
		t.Fatalf("expected parent fallback properties, got %#v", propertyMap)
	}
	if _, ok := propertyMap["blankValue"]; ok {
		t.Fatalf("expected blank-value property to be ignored, got %#v", propertyMap)
	}
}

func TestSetPomPropertyValueIgnoresBlankInputs(t *testing.T) {
	propertyMap := map[string]string{}
	setPomPropertyValue(propertyMap, "", "ignored")
	setPomPropertyValue(propertyMap, "ignored", "")
	if _, ok := propertyMap["ignored"]; ok {
		t.Fatalf("expected blank setter inputs to be ignored, got %#v", propertyMap)
	}
}

func TestPOMConsumerPoliciesAndCopies(t *testing.T) {
	data := []byte(`<project><groupId>project.group</groupId><artifactId>root</artifactId><version>5</version><parent><groupId>parent.group</groupId><version>4</version></parent><properties><project.groupId>explicit</project.groupId><blank> </blank><duplicate>1</duplicate><duplicate>2</duplicate></properties><dependencies><dependency><groupId>g</groupId><artifactId>a</artifactId></dependency></dependencies><dependencyManagement><dependencies><dependency><groupId>m</groupId><artifactId>b</artifactId></dependency></dependencies></dependencyManagement></project>`)
	pom, err := DecodePOM(data)
	if err != nil {
		t.Fatal(err)
	}
	for i := range data {
		data[i] = 'x'
	}
	inventory, identity := pom.InventoryPolicy(), pom.IdentityPolicy()
	assertPOMProperties(t, inventory.Properties, map[string]string{
		"project.groupId": "project.group", "pom.groupId": "project.group", "groupId": "project.group",
		"project.version": "5", "pom.version": "5", "version": "5",
		"project.artifactId": "root", "pom.artifactId": "root", "artifactId": "root",
		"project.parent.groupId": "parent.group", "project.parent.version": "4",
	})
	if identity.Properties["project.groupId"] != "explicit" || identity.Properties["duplicate"] != "2" {
		t.Fatal("explicit property policy changed")
	}
	if _, ok := inventory.Properties["blank"]; ok {
		t.Fatal("inventory retained blank")
	}
	if value, ok := identity.Properties["blank"]; !ok || value != "" {
		t.Fatal("identity dropped blank")
	}
	inventory.Properties["project.groupId"] = "changed"
	inventory.Dependencies[0].ArtifactID = "changed"
	identity.ManagedDependencies[0].ArtifactID = "changed"
	if next := pom.InventoryPolicy(); next.Properties["project.groupId"] != "project.group" || next.Dependencies[0].ArtifactID != "a" || next.ManagedDependencies[0].ArtifactID != "b" {
		t.Fatal("mutable view escaped")
	}
	serialized, err := json.Marshal(struct{ Evidence ParsedPOM }{Evidence: pom})
	if err != nil || string(serialized) != `{"Evidence":{}}` {
		t.Fatalf("private evidence serialized: %s %v", serialized, err)
	}
}

func TestPOMConcurrentConsumerViews(t *testing.T) {
	pom, err := DecodePOM([]byte(`<project><properties><x>original</x></properties><dependencies><dependency><artifactId>original</artifactId></dependency></dependencies></project>`))
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			for range 32 {
				view := pom.IdentityPolicy()
				view.Properties["x"] = "changed"
				view.Dependencies[0].ArtifactID = "changed"
				other := pom.InventoryPolicy()
				if other.Properties["x"] != "original" || other.Dependencies[0].ArtifactID != "original" {
					t.Error("consumer mutation crossed ownership")
				}
			}
		})
	}
	group.Wait()
}

func TestPOMZeroValuePolicyParity(t *testing.T) {
	var pom ParsedPOM
	copied := pom
	pointer := &pom
	want := POMConsumerView{Properties: map[string]string{}}
	views := []POMConsumerView{
		pom.InventoryPolicy(), pom.IdentityPolicy(),
		copied.InventoryPolicy(), copied.IdentityPolicy(),
		pointer.InventoryPolicy(), pointer.IdentityPolicy(),
	}
	for i, view := range views {
		if !reflect.DeepEqual(view, want) {
			t.Fatalf("zero policy %d = %#v, want %#v", i, view, want)
		}
		view.Properties["changed"] = "caller-owned"
	}
	for _, view := range []POMConsumerView{pom.InventoryPolicy(), pom.IdentityPolicy(), copied.InventoryPolicy(), copied.IdentityPolicy()} {
		if !reflect.DeepEqual(view, want) {
			t.Fatalf("zero policy mutation escaped: %#v", view)
		}
	}
}

func TestPOMCopiedValueAndPointerPolicyParity(t *testing.T) {
	pom, err := DecodePOM([]byte(`<project><properties><x>first</x><x>last</x><blank> </blank></properties><dependencies><dependency><artifactId>direct</artifactId></dependency></dependencies><dependencyManagement><dependencies><dependency><artifactId>managed</artifactId></dependency></dependencies></dependencyManagement></project>`))
	if err != nil {
		t.Fatal(err)
	}
	copied := pom
	pointer := &pom
	cases := []struct {
		name       string
		policy     func(*ParsedPOM) POMConsumerView
		views      []POMConsumerView
		properties map[string]string
	}{
		{"inventory", (*ParsedPOM).InventoryPolicy, []POMConsumerView{pom.InventoryPolicy(), copied.InventoryPolicy(), pointer.InventoryPolicy()}, map[string]string{"x": "last"}},
		{"identity", (*ParsedPOM).IdentityPolicy, []POMConsumerView{pom.IdentityPolicy(), copied.IdentityPolicy(), pointer.IdentityPolicy()}, map[string]string{"x": "last", "blank": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := POMConsumerView{
				Properties:          tc.properties,
				Dependencies:        []POMDependency{{ArtifactID: "direct"}},
				ManagedDependencies: []POMDependency{{ArtifactID: "managed"}},
			}
			assertPOMPolicyViewsAreCallerOwned(t, tc.views, want)
			for _, source := range []*ParsedPOM{&pom, &copied, pointer} {
				if view := tc.policy(source); !reflect.DeepEqual(view, want) {
					t.Fatalf("copied policy mutation escaped: %#v", view)
				}
			}
		})
	}
}

func assertPOMPolicyViewsAreCallerOwned(t *testing.T, views []POMConsumerView, want POMConsumerView) {
	t.Helper()
	for i, view := range views {
		if !reflect.DeepEqual(view, want) {
			t.Fatalf("policy %d = %#v, want %#v", i, view, want)
		}
		view.Properties["x"] = "changed"
		view.Dependencies[0].ArtifactID = "changed"
		view.ManagedDependencies[0].ArtifactID = "changed"
	}
}

func assertPOMProperties(t *testing.T, actual, expected map[string]string) {
	t.Helper()
	for key, want := range expected {
		if got := actual[key]; got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}
