// Based on https://github.com/morvencao/kube-mutating-webhook-tutorial/

package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"

	"github.com/fsnotify/fsnotify"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"go.uber.org/zap"
	"k8s.io/api/admission/v1beta1"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
)

var (
	runtimeScheme = runtime.NewScheme()
	codecs        = serializer.NewCodecFactory(runtimeScheme)
	deserializer  = codecs.UniversalDeserializer()
)

var ignoredNamespaces = []string{
	metav1.NamespaceSystem,
	metav1.NamespacePublic,
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

type podMutator interface {
	mutate(pod *corev1.Pod) ([]patchOperation, error)
}

// WebhookServer is a webhook server that can accept requests from the Apiserver
type WebhookServer struct {
	certFile    string
	keyFile     string
	cert        *tls.Certificate
	clusterName string
	logger      *zap.SugaredLogger
	mu          sync.RWMutex
	server      *http.Server
	certWatcher *fsnotify.Watcher
	mutators    []podMutator
}

func (whsvr *WebhookServer) getCert(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	whsvr.mu.Lock()
	defer whsvr.mu.Unlock()
	return whsvr.cert, nil
}

func init() {
	_ = corev1.AddToScheme(runtimeScheme)
	_ = admissionregistrationv1beta1.AddToScheme(runtimeScheme)
	_ = corev1.AddToScheme(runtimeScheme)
}

// Check whether the target resoured need to be mutated
func mutationRequired(ignoredList []string, metadata *metav1.ObjectMeta) bool {
	// skip special kubernetes system namespaces
	for _, namespace := range ignoredList {
		if metadata.Namespace == namespace {
			return false
		}
	}
	return true
}

type code interface {
	Code() int
}

func errorCode(err error) int {
	if me, ok := err.(code); ok {
		return me.Code()
	}
	return http.StatusInternalServerError
}

// Serve method for webhook server
func (whsvr *WebhookServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body []byte

	if whsvr.logger == nil {
		whsvr.logger = zap.NewNop().Sugar()
	}

	if r.Body != nil {
		if data, err := ioutil.ReadAll(r.Body); err == nil {
			body = data
		}
	}
	if len(body) == 0 {
		whsvr.logger.Error("empty body")
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	// verify the content type is accurate
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		whsvr.logger.Errorw("invalid content type", "expected", "application/json", "context type", contentType)
		http.Error(w, "invalid Content-Type, expect `application/json`", http.StatusUnsupportedMediaType)
		return
	}

	admissionReviewResponse := v1beta1.AdmissionReview{
		Response: &v1beta1.AdmissionResponse{
			Allowed: true, // Always allow the creation of the pod since this webhook does not act as Validating Webhook.
		},
	}

	admissionReviewRequest := v1beta1.AdmissionReview{}
	if _, _, err := deserializer.Decode(body, nil, &admissionReviewRequest); err != nil {
		whsvr.logger.Errorw("can't decode body", "err", err, "body", body)
		http.Error(w, fmt.Sprintf("could not decode request body: %q", err.Error()), http.StatusBadRequest)
		return
	}

	if len(admissionReviewRequest.Request.Object.Raw) == 0 {
		whsvr.logger.Errorw("object not present in request body", "body", body)
		http.Error(w, fmt.Sprintf("object not present in request body: %q", body), http.StatusBadRequest)
		return
	}

	req := admissionReviewRequest.Request
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		whsvr.logger.Errorw("could not unmarshal raw object", "err", err, "object", string(req.Object.Raw))
		return
	}
	// workaround for empty namespace on the pod level
	if pod.Namespace == "" {
		pod.Namespace = req.Namespace
	}

	whsvr.logger.Infow("received admission review", "kind", req.Kind, "namespace", req.Namespace, "name",
		req.Name, "pod", pod.Name, "UID", req.UID, "operation", req.Operation, "userinfo", req.UserInfo)

	// determine whether to perform mutation
	if !mutationRequired(ignoredNamespaces, &pod.ObjectMeta) {
		whsvr.logger.Infow("skipped mutation", "namespace", pod.Namespace, "pod", pod.Name, "reason", "policy check (special namespaces)")
		return
	}

	var patches []patchOperation
	for _, m := range whsvr.mutators {
		p, err := m.mutate(&pod)
		if err != nil {
			whsvr.logger.Errorw("error during mutation", "err", err)
			http.Error(w, fmt.Sprintf("error during mutation: %q", err.Error()), errorCode(err))
			return
		}
		patches = append(patches, p...)
	}

	if len(patches) > 0 {
		patchBytes, err := json.Marshal(patches)
		if err != nil {
			whsvr.logger.Errorw("error marshaling patch", "err", err)
		}
		admissionReviewResponse.Response.Patch = patchBytes
		admissionReviewResponse.Response.PatchType = func() *v1beta1.PatchType {
			pt := v1beta1.PatchTypeJSONPatch // Only PatchTypeJSONPatch is allowed by now.
			return &pt
		}()
	}

	if admissionReviewRequest.Request != nil {
		admissionReviewResponse.Response.UID = admissionReviewRequest.Request.UID
	}

	resp, err := json.Marshal(admissionReviewResponse)
	if err != nil {
		whsvr.logger.Errorw("can't decode response", "err", err)
		http.Error(w, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
		return
	}
	whsvr.logger.Info("writing reponse")
	if _, err := w.Write(resp); err != nil {
		whsvr.logger.Errorw("can't write response", "err", err)
		http.Error(w, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
		return
	}
}
