package server

import (
	"bytes"
	json_encoding "encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"

	"k8s.io/api/admission/v1beta1"
	corev1 "k8s.io/api/core/v1"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
)

const (
	integrationConfig = `integration_name: com.newrelic.nginx
instances:
  - name: nginx-server-metrics
    command: metrics
    arguments:
      status_url: http://127.0.0.1/status`
)

func loadTestData(t *testing.T, name string) []byte {
	data, err := ioutil.ReadFile(path.Join("testdata", name))
	if err != nil {
		t.Fatalf("cannot read testdata file: %v", err)
	}
	var buffer bytes.Buffer
	if len(data) > 0 {
		if err := json_encoding.Compact(&buffer, data); err != nil {
			t.Fatalf(err.Error())
		}
	}
	return buffer.Bytes()
}

func TestServeHTTP(t *testing.T) {
	expectedEnvVarsPatchForValidBody := loadTestData(t, "expectedEnvVarsAdmissionReviewPatch.json")
	expectedSidecarPatchForValidBody := loadTestData(t, "expectedSidecarAdmissionReviewPatch.json")
	missingObjectRequestBody := bytes.Replace(makeTestData(t, "default", map[string]string{}), []byte("\"object\""), []byte("\"foo\""), -1)
	configName := "my-config"

	patchTypeForValidBody := v1beta1.PatchTypeJSONPatch
	cases := []struct {
		name                      string
		requestBody               []byte
		contentType               string
		expectedStatusCode        int
		expectedBodyWhenHTTPError string
		expectedAdmissionReview   v1beta1.AdmissionReview
	}{
		{
			name:               "mutation applied - valid body",
			requestBody:        makeTestData(t, "default", nil),
			contentType:        "application/json",
			expectedStatusCode: http.StatusOK,
			expectedAdmissionReview: v1beta1.AdmissionReview{
				Response: &v1beta1.AdmissionResponse{
					UID:       types.UID(1),
					Allowed:   true,
					Result:    nil,
					Patch:     expectedEnvVarsPatchForValidBody,
					PatchType: &patchTypeForValidBody,
				},
			},
		},
		{
			name:               "mutation not applied - valid body for ignored namespaces",
			requestBody:        makeTestData(t, "kube-system", nil),
			contentType:        "application/json",
			expectedStatusCode: http.StatusOK,
			expectedAdmissionReview: v1beta1.AdmissionReview{
				Response: &v1beta1.AdmissionResponse{
					UID:       types.UID(1),
					Allowed:   true,
					Result:    nil,
					Patch:     nil,
					PatchType: nil,
				},
			},
		},
		{
			name:                      "empty body",
			contentType:               "application/json",
			expectedStatusCode:        http.StatusBadRequest,
			expectedBodyWhenHTTPError: "empty body" + "\n",
		},
		{
			name:                      "wrong content-type",
			requestBody:               makeTestData(t, "default", nil),
			contentType:               "application/yaml",
			expectedStatusCode:        http.StatusUnsupportedMediaType,
			expectedBodyWhenHTTPError: "invalid Content-Type, expect `application/json`" + "\n",
		},
		{
			name:                      "invalid body",
			requestBody:               []byte{0, 1, 2},
			contentType:               "application/json",
			expectedStatusCode:        http.StatusBadRequest,
			expectedBodyWhenHTTPError: "could not decode request body: \"yaml: control characters are not allowed\"\n",
		},
		{
			name:                      "mutation fails - object not present in request body",
			requestBody:               missingObjectRequestBody,
			contentType:               "application/json",
			expectedStatusCode:        http.StatusBadRequest,
			expectedBodyWhenHTTPError: fmt.Sprintf("object not present in request body: %q\n", missingObjectRequestBody),
		},
		{
			name:               "sidecar mutation applied - with sidecar",
			requestBody:        makeTestData(t, "default", map[string]string{"newrelic.com/integrations-sidecar-configmap": configName}),
			contentType:        "application/json",
			expectedStatusCode: http.StatusOK,
			expectedAdmissionReview: v1beta1.AdmissionReview{
				Response: &v1beta1.AdmissionResponse{
					UID:       types.UID(1),
					Allowed:   true,
					Result:    nil,
					Patch:     expectedSidecarPatchForValidBody,
					PatchType: &patchTypeForValidBody,
				},
			},
		},
		{
			name:                      "sidecar mutation - wrong config map name",
			requestBody:               makeTestData(t, "default", map[string]string{"newrelic.com/integrations-sidecar-configmap": "wrong"}),
			contentType:               "application/json",
			expectedStatusCode:        http.StatusBadRequest,
			expectedBodyWhenHTTPError: fmt.Sprintf("error during mutation: \"config map: 'wrong', not found\"\n"),
		},
	}

	clusterName := "foobar"
	whsvr := &Webhook{
		ClusterName: clusterName,
		Server:      &http.Server{},
		Mutators: []podMutator{
			NewEnvVarMutator(clusterName),
			NewSidecarMutator(clusterName, makeConfigMapRetriever("default", configName, map[string]string{"config.yaml": integrationConfig})),
		},
		IgnoreNamespaces: []string{metav1.NamespaceSystem, metav1.NamespacePublic},
	}

	server := httptest.NewServer(whsvr)
	defer server.Close()

	for i, c := range cases {
		t.Run(fmt.Sprintf("[%d] %s", i, c.name), func(t *testing.T) {

			fmt.Println(c.name)
			resp, err := http.Post(server.URL, c.contentType, bytes.NewReader(c.requestBody))
			assert.NoError(t, err)
			assert.Equal(t, c.expectedStatusCode, resp.StatusCode)

			gotBody, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("could not read body: %v", err)
			}
			var gotReview v1beta1.AdmissionReview
			if err := json.Unmarshal(gotBody, &gotReview); err != nil {
				assert.Equal(t, c.expectedBodyWhenHTTPError, string(gotBody))
				return
			}

			assert.Equal(t, c.expectedAdmissionReview, gotReview)
		})
	}

}

func TestServeHTTPIgnoreNamespaces(t *testing.T) {
	expectedEnvVarsPatchForValidBody := loadTestData(t, "expectedEnvVarsAdmissionReviewPatch.json")
	configName := "my-config"

	patchTypeForValidBody := v1beta1.PatchTypeJSONPatch
	cases := []struct {
		name                      string
		requestBody               []byte
		contentType               string
		expectedStatusCode        int
		expectedBodyWhenHTTPError string
		expectedAdmissionReview   v1beta1.AdmissionReview
	}{
		{
			name:               "mutation applied - namespace kube-system",
			requestBody:        makeTestData(t, "kube-system", nil),
			contentType:        "application/json",
			expectedStatusCode: http.StatusOK,
			expectedAdmissionReview: v1beta1.AdmissionReview{
				Response: &v1beta1.AdmissionResponse{
					UID:       types.UID(1),
					Allowed:   true,
					Result:    nil,
					Patch:     expectedEnvVarsPatchForValidBody,
					PatchType: &patchTypeForValidBody,
				},
			},
		},
		{
			name:               "mutation applied - namespace kube-public",
			requestBody:        makeTestData(t, "kube-public", nil),
			contentType:        "application/json",
			expectedStatusCode: http.StatusOK,
			expectedAdmissionReview: v1beta1.AdmissionReview{
				Response: &v1beta1.AdmissionResponse{
					UID:       types.UID(1),
					Allowed:   true,
					Result:    nil,
					Patch:     expectedEnvVarsPatchForValidBody,
					PatchType: &patchTypeForValidBody,
				},
			},
		},
		{
			name:               "mutation not applied - namespace testing ignored",
			requestBody:        makeTestData(t, "testing", nil),
			contentType:        "application/json",
			expectedStatusCode: http.StatusOK,
			expectedAdmissionReview: v1beta1.AdmissionReview{
				Response: &v1beta1.AdmissionResponse{
					UID:       types.UID(1),
					Allowed:   true,
					Result:    nil,
					Patch:     nil,
					PatchType: nil,
				},
			},
		},
	}

	clusterName := "foobar"
	whsvr := &Webhook{
		ClusterName: clusterName,
		Server:      &http.Server{},
		Mutators: []podMutator{
			NewEnvVarMutator(clusterName),
			NewSidecarMutator(clusterName, makeConfigMapRetriever("default", configName, map[string]string{"config.yaml": integrationConfig})),
		},
		IgnoreNamespaces: []string{"testing"},
	}

	server := httptest.NewServer(whsvr)
	defer server.Close()

	for i, c := range cases {
		t.Run(fmt.Sprintf("[%d] %s", i, c.name), func(t *testing.T) {

			fmt.Println(c.name)
			resp, err := http.Post(server.URL, c.contentType, bytes.NewReader(c.requestBody))
			assert.NoError(t, err)
			assert.Equal(t, c.expectedStatusCode, resp.StatusCode)

			gotBody, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("could not read body: %v", err)
			}
			var gotReview v1beta1.AdmissionReview
			if err := json.Unmarshal(gotBody, &gotReview); err != nil {
				assert.Equal(t, c.expectedBodyWhenHTTPError, string(gotBody))
				return
			}

			assert.Equal(t, c.expectedAdmissionReview, gotReview)
		})
	}
}

func Benchmark_EnvVarWebhookPerformance(b *testing.B) {
	body := makeTestData(b, "default", map[string]string{})

	clusterName := "foobar"
	whsvr := &Webhook{
		ClusterName: clusterName,
		Server: &http.Server{
			Addr: ":8080",
		},
		Mutators: []podMutator{
			NewEnvVarMutator(clusterName),
		},
	}

	server := httptest.NewServer(whsvr)
	defer server.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		http.Post(server.URL, "application/json", bytes.NewReader(body)) //nolint: errcheck
	}
}

func Benchmark_SidecarWebhookPerformance(b *testing.B) {
	namespace := "default"
	configName := "my-config"
	body := makeTestData(b, namespace, map[string]string{"newrelic.com/integrations-sidecar-configmap": configName})
	clusterName := "mycluster"
	whsvr := &Webhook{
		ClusterName: clusterName,
		Server: &http.Server{
			Addr: ":8080",
		},
		Mutators: []podMutator{
			NewSidecarMutator(clusterName, makeConfigMapRetriever(namespace, configName, map[string]string{"config.yaml": integrationConfig})),
		},
	}

	server := httptest.NewServer(whsvr)
	defer server.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		http.Post(server.URL, "application/json", bytes.NewReader(body)) //nolint: errcheck
	}
}

func makeTestData(t testing.TB, namespace string, annotations map[string]string) []byte {
	t.Helper()

	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "test-123-123",
			GenerateName:    "test-123-123", // required for creating metadata for deployment
			Annotations:     annotations,
			Namespace:       namespace,
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet"}}, // required for populating metadata for deployment
		},
		Spec: corev1.PodSpec{
			Volumes:          []corev1.Volume{{Name: "v0"}},
			InitContainers:   []corev1.Container{{Name: "c0"}},
			Containers:       []corev1.Container{{Name: "c1", Image: "newrelic/image:latest"}, {Name: "c2", Image: "newrelic/image2:1.0.0"}},
			ImagePullSecrets: []corev1.LocalObjectReference{{Name: "p0"}},
		},
	}

	raw, err := json.Marshal(&pod)
	if err != nil {
		t.Fatalf("Could not create test pod: %v", err)
	}

	review := v1beta1.AdmissionReview{
		Request: &v1beta1.AdmissionRequest{
			Kind: metav1.GroupVersionKind{},
			Object: runtime.RawExtension{
				Raw: raw,
			},
			Operation: v1beta1.Create,
			UID:       types.UID(1),
		},
	}
	reviewJSON, err := json.Marshal(review)
	if err != nil {
		t.Fatalf("Failed to create AdmissionReview: %v", err)
	}
	return reviewJSON
}

type dummyCfgMapRetriever struct {
	namespace string
	name      string
	data      map[string]string
}

func (dcr *dummyCfgMapRetriever) ConfigMap(namespace, name string) (*corev1.ConfigMap, error) {
	if dcr.namespace == namespace && dcr.name == name {
		return &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      name,
			},
			Data: dcr.data,
		}, nil
	}
	return nil, k8s_errors.NewNotFound(schema.GroupResource{}, name)
}

func makeConfigMapRetriever(namespace, name string, data map[string]string) configMapRetriever {
	return &dummyCfgMapRetriever{
		namespace: namespace,
		name:      name,
		data:      data,
	}
}
