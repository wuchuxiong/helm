/*
Copyright The Helm Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package driver // import "helm.sh/helm/v3/pkg/storage/driver"

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"text/template"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	sq "github.com/Masterminds/squirrel"
	"github.com/jmoiron/sqlx"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kblabels "k8s.io/apimachinery/pkg/labels"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"

	rspb "helm.sh/helm/v3/pkg/release"
)

var multiReleaseTestTmpl = `
{{.}}:
  {{.}}Sub1:
    {{.}}Sub1Sub1: true
  {{.}}Sub2:
    {{.}}Sub2Sub1: "{{.}}Sub2Sub1Value"
    {{.}}Sub2Sub2: false
    {{.}}Sub2Sub3: false
    {{.}}Sub2Sub4: false
    {{.}}Sub2Sub5: "{{.}}Sub2Sub5Value"
    {{.}}Sub2Sub6: "{{.}}Sub2Sub6Value"
`

func multiReleaseStub(name string, vers int, namespace string, status rspb.Status) *rspb.Release {
	var buf bytes.Buffer
	tmpl, _ := template.New("").Parse(multiReleaseTestTmpl)
	for i := 0; i < 100; i++ {
		tmpl.Execute(&buf, fmt.Sprintf("field%d", i))
	}

	return &rspb.Release{
		Name:      name,
		Version:   vers,
		Namespace: namespace,
		Info:      &rspb.Info{Status: status},
		Manifest:  buf.String(),
	}
}

func releaseStub(name string, vers int, namespace string, status rspb.Status) *rspb.Release {
	return &rspb.Release{
		Name:      name,
		Version:   vers,
		Namespace: namespace,
		Info:      &rspb.Info{Status: status},
	}
}

func testKey(name string, vers int) string {
	return fmt.Sprintf("%s.v%d", name, vers)
}

func tsFixtureMemory(t *testing.T) *Memory {
	hs := []*rspb.Release{
		// rls-a
		releaseStub("rls-a", 4, "default", rspb.StatusDeployed),
		releaseStub("rls-a", 1, "default", rspb.StatusSuperseded),
		releaseStub("rls-a", 3, "default", rspb.StatusSuperseded),
		releaseStub("rls-a", 2, "default", rspb.StatusSuperseded),
		// rls-b
		releaseStub("rls-b", 4, "default", rspb.StatusDeployed),
		releaseStub("rls-b", 1, "default", rspb.StatusSuperseded),
		releaseStub("rls-b", 3, "default", rspb.StatusSuperseded),
		releaseStub("rls-b", 2, "default", rspb.StatusSuperseded),
		// rls-c in other namespace
		releaseStub("rls-c", 4, "mynamespace", rspb.StatusDeployed),
		releaseStub("rls-c", 1, "mynamespace", rspb.StatusSuperseded),
		releaseStub("rls-c", 3, "mynamespace", rspb.StatusSuperseded),
		releaseStub("rls-c", 2, "mynamespace", rspb.StatusSuperseded),
	}

	mem := NewMemory()
	for _, tt := range hs {
		err := mem.Create(testKey(tt.Name, tt.Version), tt)
		if err != nil {
			t.Fatalf("Test setup failed to create: %s\n", err)
		}
	}
	return mem
}

// newTestFixture initializes a MockConfigMapsInterface.
// ConfigMaps are created for each release provided.
func newTestFixtureCfgMaps(t *testing.T, releases ...*rspb.Release) *ConfigMaps {
	var mock MockConfigMapsInterface
	mock.Init(t, releases...)

	return NewConfigMaps(&mock)
}

// MockConfigMapsInterface mocks a kubernetes ConfigMapsInterface
type MockConfigMapsInterface struct {
	corev1.ConfigMapInterface

	objects map[string]*v1.ConfigMap
}

// Init initializes the MockConfigMapsInterface with the set of releases.
func (mock *MockConfigMapsInterface) Init(t *testing.T, releases ...*rspb.Release) {
	mock.objects = map[string]*v1.ConfigMap{}

	for _, rls := range releases {
		objkey := testKey(rls.Name, rls.Version)

		cfgmap, err := newConfigMapsObject(objkey, rls, nil)
		if err != nil {
			t.Fatalf("Failed to create configmap: %s", err)
		}
		mock.objects[objkey] = cfgmap
	}
}

// Get returns the ConfigMap by name.
func (mock *MockConfigMapsInterface) Get(_ context.Context, name string, _ metav1.GetOptions) (*v1.ConfigMap, error) {
	object, ok := mock.objects[name]
	if !ok {
		return nil, apierrors.NewNotFound(v1.Resource("tests"), name)
	}
	return object, nil
}

// List returns the a of ConfigMaps.
func (mock *MockConfigMapsInterface) List(_ context.Context, opts metav1.ListOptions) (*v1.ConfigMapList, error) {
	var list v1.ConfigMapList

	labelSelector, err := kblabels.Parse(opts.LabelSelector)
	if err != nil {
		return nil, err
	}

	for _, cfgmap := range mock.objects {
		if labelSelector.Matches(kblabels.Set(cfgmap.ObjectMeta.Labels)) {
			list.Items = append(list.Items, *cfgmap)
		}
	}
	return &list, nil
}

// Create creates a new ConfigMap.
func (mock *MockConfigMapsInterface) Create(_ context.Context, cfgmap *v1.ConfigMap, _ metav1.CreateOptions) (*v1.ConfigMap, error) {
	name := cfgmap.ObjectMeta.Name
	if object, ok := mock.objects[name]; ok {
		return object, apierrors.NewAlreadyExists(v1.Resource("tests"), name)
	}
	mock.objects[name] = cfgmap
	return cfgmap, nil
}

// Update updates a ConfigMap.
func (mock *MockConfigMapsInterface) Update(_ context.Context, cfgmap *v1.ConfigMap, _ metav1.UpdateOptions) (*v1.ConfigMap, error) {
	name := cfgmap.ObjectMeta.Name
	if _, ok := mock.objects[name]; !ok {
		return nil, apierrors.NewNotFound(v1.Resource("tests"), name)
	}
	mock.objects[name] = cfgmap
	return cfgmap, nil
}

// Delete deletes a ConfigMap by name.
func (mock *MockConfigMapsInterface) Delete(_ context.Context, name string, _ metav1.DeleteOptions) error {
	if _, ok := mock.objects[name]; !ok {
		return apierrors.NewNotFound(v1.Resource("tests"), name)
	}
	delete(mock.objects, name)
	return nil
}

// newTestFixture initializes a MockSecretsInterface.
// Secrets are created for each release provided.
func newTestFixtureSecrets(t *testing.T, releases ...*rspb.Release) *Secrets {
	var mock MockSecretsInterface
	mock.Init(t, releases...)

	return NewSecrets(&mock)
}

// MockSecretsInterface mocks a kubernetes SecretsInterface
type MockSecretsInterface struct {
	corev1.SecretInterface

	objects map[string]*v1.Secret
}

// Init initializes the MockSecretsInterface with the set of releases.
func (mock *MockSecretsInterface) Init(t *testing.T, releases ...*rspb.Release) {
	mock.objects = map[string]*v1.Secret{}

	for _, rls := range releases {
		objkey := testKey(rls.Name, rls.Version)

		secret, err := newSecretsObject(objkey, rls, nil)
		if err != nil {
			t.Fatalf("Failed to create secret: %s", err)
		}
		mock.objects[objkey] = secret
	}
}

// Get returns the Secret by name.
func (mock *MockSecretsInterface) Get(_ context.Context, name string, _ metav1.GetOptions) (*v1.Secret, error) {
	object, ok := mock.objects[name]
	if !ok {
		return nil, apierrors.NewNotFound(v1.Resource("tests"), name)
	}
	return object, nil
}

// List returns the a of Secret.
func (mock *MockSecretsInterface) List(_ context.Context, opts metav1.ListOptions) (*v1.SecretList, error) {
	var list v1.SecretList

	labelSelector, err := kblabels.Parse(opts.LabelSelector)
	if err != nil {
		return nil, err
	}

	for _, secret := range mock.objects {
		if labelSelector.Matches(kblabels.Set(secret.ObjectMeta.Labels)) {
			list.Items = append(list.Items, *secret)
		}
	}
	return &list, nil
}

// Create creates a new Secret.
func (mock *MockSecretsInterface) Create(_ context.Context, secret *v1.Secret, _ metav1.CreateOptions) (*v1.Secret, error) {
	name := secret.ObjectMeta.Name
	if object, ok := mock.objects[name]; ok {
		return object, apierrors.NewAlreadyExists(v1.Resource("tests"), name)
	}
	mock.objects[name] = secret
	return secret, nil
}

// Update updates a Secret.
func (mock *MockSecretsInterface) Update(_ context.Context, secret *v1.Secret, _ metav1.UpdateOptions) (*v1.Secret, error) {
	name := secret.ObjectMeta.Name
	if _, ok := mock.objects[name]; !ok {
		return nil, apierrors.NewNotFound(v1.Resource("tests"), name)
	}
	mock.objects[name] = secret
	return secret, nil
}

// Delete deletes a Secret by name.
func (mock *MockSecretsInterface) Delete(_ context.Context, name string, _ metav1.DeleteOptions) error {
	if _, ok := mock.objects[name]; !ok {
		return apierrors.NewNotFound(v1.Resource("tests"), name)
	}
	delete(mock.objects, name)
	return nil
}

// newTestFixtureMultiSecrets initializes a MockMultiSecretsInterface.
// MultiSecrets are created for each release provided.
func newTestFixtureMultiSecrets(t *testing.T, releases ...*rspb.Release) *MultiSecrets {
	var mock MockMultiSecretsInterface
	mock.Init(t, releases...)

	return NewMultiSecrets(&mock)
}

// MockMultiSecretsInterface mocks a kubernetes SecretsInterface
type MockMultiSecretsInterface struct {
	corev1.SecretInterface

	objects map[string]*v1.Secret
}

// Init initializes the MockMultiSecretsInterface with the set of releases.
func (mock *MockMultiSecretsInterface) Init(t *testing.T, releases ...*rspb.Release) {
	mock.objects = map[string]*v1.Secret{}

	for _, rls := range releases {
		objkey := testKey(rls.Name, rls.Version)

		secrets, err := newMultiSecretsObject(objkey, rls, nil, 1024)
		if err != nil {
			t.Fatalf("Failed to create secrets: %s", err)
		}
		for _, se := range *secrets {
			v := se
			mock.objects[se.Name] = &v
		}
	}
}

// Get returns the Secret by name.
func (mock *MockMultiSecretsInterface) Get(_ context.Context, name string, _ metav1.GetOptions) (*v1.Secret, error) {
	object, ok := mock.objects[name]
	if !ok {
		return nil, apierrors.NewNotFound(v1.Resource("tests"), name)
	}
	return object, nil
}

// List returns the a of Secret.
func (mock *MockMultiSecretsInterface) List(_ context.Context, opts metav1.ListOptions) (*v1.SecretList, error) {
	var list v1.SecretList

	labelSelector, err := kblabels.Parse(opts.LabelSelector)
	if err != nil {
		return nil, err
	}

	for _, secret := range mock.objects {
		temp := *secret
		if labelSelector.Matches(kblabels.Set(temp.ObjectMeta.Labels)) {
			list.Items = append(list.Items, temp)
		}
	}
	return &list, nil
}

// Create creates a new Secret.
func (mock *MockMultiSecretsInterface) Create(_ context.Context, secret *v1.Secret, _ metav1.CreateOptions) (*v1.Secret, error) {
	name := secret.ObjectMeta.Name
	if object, ok := mock.objects[name]; ok {
		return object, apierrors.NewAlreadyExists(v1.Resource("tests"), name)
	}
	mock.objects[name] = secret
	return secret, nil
}

// Update updates a Secret.
func (mock *MockMultiSecretsInterface) Update(_ context.Context, secret *v1.Secret, _ metav1.UpdateOptions) (*v1.Secret, error) {
	name := secret.ObjectMeta.Name
	temp := *secret
	if _, ok := mock.objects[name]; !ok {
		return nil, apierrors.NewNotFound(v1.Resource("tests"), name)
	}
	mock.objects[name] = &temp
	return secret, nil
}

// Delete deletes a Secret by name.
func (mock *MockMultiSecretsInterface) Delete(_ context.Context, name string, _ metav1.DeleteOptions) error {
	if _, ok := mock.objects[name]; !ok {
		return apierrors.NewNotFound(v1.Resource("tests"), name)
	}
	delete(mock.objects, name)
	return nil
}

// newTestFixtureSQL mocks the SQL database (for testing purposes)
func newTestFixtureSQL(t *testing.T, releases ...*rspb.Release) (*SQL, sqlmock.Sqlmock) {
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("error when opening stub database connection: %v", err)
	}

	sqlxDB := sqlx.NewDb(sqlDB, "sqlmock")
	return &SQL{
		db:               sqlxDB,
		Log:              func(a string, b ...interface{}) {},
		namespace:        "default",
		statementBuilder: sq.StatementBuilder.PlaceholderFormat(sq.Dollar),
	}, mock
}
