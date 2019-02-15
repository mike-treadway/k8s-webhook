# Kubernetes webhook for New Relic

## How does it work?

The webhook intercepts POD creation requests to the Kubernetes API and mutates them in the following ways:

* Injects the following environment variables needed by the APM agents to identify Kubernetes objects

    - `NEW_RELIC_METADATA_KUBERNETES_CLUSTER_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_NODE_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_NAMESPACE_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_DEPLOYMENT_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_POD_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_CONTAINER_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_CONTAINER_IMAGE_NAME`

    These environment variables can be set manually by the customer, or they can be automatically injected using a MutatingAdmissionWebhook.
    New Relic provides an easy method for deploying this automatic approach.

* Enables the monitoring of services running in pods, by injecting a sidecar containing the agent and relevant integration into all marked pods.
## Setup

### 1) Check if MutatingAdmissionWebhook is enabled on your cluster

This feature requires Kubernetes 1.9 or later. Verify that the kube-apiserver process has the admission-control flag set.

```
$ kubectl api-versions | grep admissionregistration.k8s.io/v1beta1
admissionregistration.k8s.io/v1beta1
```

### 2) Install the injection

```bash
$ kubectl apply -f deploy/newrelic-webhook.yaml
```

Executing this:

- creates `newrelic-webhook-deployment` and `newrelic-webhook-svc`.
- registers the `newrelic-webhook-svc` service as a MutatingAdmissionWebhook with the Kubernetes API.

### 3) Install the certificates

This webhook needs to be authenticated by the Kubernetes extension API server, so it will need to have a signed certificate from a CA trusted by the extension API server. The certificate management is isolated from the webhook server and a secret is used to mount them. 

**Important**: the webhook server has a file watcher pointed at the secret's folder that will trigger a certificate reload whenever anything is created or modified inside the secret. This allows easy certificate rotation with an update of the TLS secret that is created by running:

```bash
$ namespace=default # Change the namespace here if you also changed it in the yaml files.
$ serverCert=$(kubectl get csr newrelic-webhook-svc.${namespace} -o jsonpath='{.status.certificate}')
$ tmpdir=$(mktemp -d)
$ echo ${serverCert} | openssl base64 -d -A -out ${tmpdir}/server-cert.pem
$ kubectl patch secret newrelic-webhook-secret --type='json' \
    -p "[{'op': 'replace', 'path':'/data/tls.crt', 'value':'$(serverCert)'}]"
$ rm -rf $(tmpdir)
```

#### Automatic management

The certificate management can be automatic, using the Kubernetes extension API server (recommended, but optional):

```bash
$ kubectl apply -f deploy/job.yaml
```

This manifest contains a service account that has the following **cluster** permissions (**RBAC based**) to be capable of automatically manage the certificates:

* `MutatingWebhookConfiguration` - **get**, **create** and **patch**: to be able to create the webhook and patch its CA bundle.
* `CertificateSigningRequests` - **create**, **get** and **delete**: to be able to sign the certificate required for the webhook server without leaving duplicates.
* `CertificateSigningRequests/Approval` - **update**: to be able to approve CertificateSigningRequests.
* `Secrets` - **create**, **get** and **patch**: to be able to manage the TLS secret used to store the key/cert pair used in the webhook server.
* `ConfigMaps` - **get**: to be able go get the k8s api server's CA bundle, used in the MutatingWebhookConfiguration.

This job will execute the shell script [k8s-webhook-cert-manager/generate_certificate.sh](./k8s-webhook-cert-manager/generate_certificate.sh) to setup everything. This script will:

1. Generate a server key.
2. If there is any previous CSR (certificate signing request) for this key, it is deleted.
3. Generate a CSR for such key.
4. The signature of the key is then approved.
5. The server's certificate is fetched from the CSR and then encoded.
6. A secret of type `tls` is created with the server certificate and key.
7. The k8s extension api server's CA bundle is fetched.
8. The mutating webhook configuration for the webhook server is patched with the k8s api server's CA bundle from the previous step. This CA bundle will be used by the k8s extension api server when calling our webhook.

If you wish to learn more about TLS certificates management inside Kubernetes, check out [the official documentation for Managing TLS Certificate in a Cluster](https://kubernetes.io/docs/tasks/tls/managing-tls-in-a-cluster/#create-a-certificate-signing-request-object-to-send-to-the-kubernetes-api).

#### Manual management

Otherwise, if you are managing the certificate manually you will have to create the TLS secret with the signed certificate/key pair and patch the webhook's CA bundle:

```bash
$ kubectl create secret tls newrelic-webhook-secret \
      --key=server-key.pem \
      --cert=signed-server-cert.pem \
      --dry-run -o yaml |
  kubectl -n default apply -f -

$ caBundle=$(cat caBundle.pem | base64 | td -d '\n')
$ kubectl patch mutatingwebhookconfiguration newrelic-webhook-cfg --type='json' -p "[{'op': 'replace', 'path': '/webhooks/0/clientConfig/caBundle', 'value':'${caBundle}'}]"
```

Either certificate management choice made, the important thing is to have the secret created with the correct name and namespace. As long as this is done the webhook server will be able to pick it up.

### 3) Enable the webhook on your namespaces

The webhook will only monitor namespaces that have the `newrelic-webhook` label set to `enabled`.

```
$ kubectl label namespace <namespace> newrelic-webhook=enabled
```

### 4) Enable monitoring of pods

A sidecar will be injected into all pods having the `newrelic.com/integrations-sidecar-configmap` annotation set to the name of a config map object, in the same namespace as the targeted pod, containing the integration config. 
The `newrelic.com/integrations-sidecar-imagename` annotation is used to specify the sidecar image to be injected.

The injector expects the config map to have a `config.yaml` file and an optional `definition.yaml` file, which are the usual configurations for integrations.
The two will be mounted to `/var/db/newrelic-infra/integrations.d/integration.yaml` and `/var/db/newrelic-infra/newrelic-integrations/definition.yaml` respectively
and overwrite any of these if already present in the sidecar image.

Passwords and other secret information passed as arguments to the integrations can be suplied as an environment variable backed by a kubernetes secret. If the name
of an integration argument starts with `$`, the injector assumes this refers to an environment variable that is defined in the targeted pod, with the same name (minus the `$` symbol).

The agent license and other agent configuration environment variables can be added to the injector deployment and they will be all copied to the injected sidecars.

#### Example configuration:

```
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mysql-deployment
  labels:
    app: mysql
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mysql
  template:
    metadata:
      annotations:
        newrelic.com/integrations-sidecar-configmap: "mysql-newrelic-integrations-config"
        newrelic.com/integrations-sidecar-imagename: "newrelic/mysql-integration"
      labels:
        app: mysql
    spec:
      containers:
      - name: mysql
        image: mysql:5
        env:
          - name: MYSQL_ROOT_PASSWORD
            valueFrom:
              secretKeyRef:
                name: mysecret
                key: password
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: mysql-newrelic-integrations-config
  namespace: default
data:
  config.yaml: |
    integration_name: com.newrelic.mysql
    instances:
      - name: mysql-server
        command: status
        arguments:
          hostname: localhost
          port: 3306
          username: root
          password: $MYSQL_ROOT_PASSWORD
        labels:
          env: testenv
          role: write-replica
  definition.yaml: |
    name: com.newrelic.mysql
    description: Reports status and metrics for mysql server
    protocol_version: 1
    os: linux
    commands:
        status:
            command:
                - /var/db/newrelic-infra/newrelic-integrations/bin/nr-mysql
            prefix: config/mysql
            interval: 30
```

## Development

### Prerequisites

For the development process [Minikube](https://kubernetes.io/docs/getting-started-guides/minikube) and [Skaffold](https://github.com/GoogleCloudPlatform/skaffold) tools are used.

* [Install Minikube](https://kubernetes.io/docs/tasks/tools/install-minikube/).
* [Install Skaffold](https://github.com/GoogleCloudPlatform/skaffold#installation).

Currently the project compiles with **Go 1.11.4**.

### Dependency management

[Go modules](https://github.com/golang/go/wiki/Modules) are used for managing dependencies. This project does not need to be in your GOROOT, if you wish so.

Currently for K8s libraries it uses version 1.13.1. Only couple of libraries are direct dependencies, the rest are indirect. You need to point all of them to the same K8s version to make sure that everything works as expected. For the moment this process is manual.

### Configuration

* Copy the deployment file `deploy/newrelic-webhook.yaml` to `deploy/local.yaml`.
* Edit the file and set the following value as container image: `internal/newrelic-webhook-injector`.
* Make sure that `imagePullPolicy: Always` is not present in the file (otherwise, the image won't be pulled).

### Run

Run `skaffold run`. This will build a docker image, build the webhook server inside it, and finally deploy the webhook server to your Minikube and use the Kubernetes API server to sign its TLS certificate ([see section about certificates](#3-install-the-certificates)).

To follow the logs, you can run `skaffold run --tail`. To delete the resources created by Skaffold you can run `skaffold delete`.

If you would like to enable automatic redeploy on changes to the repository, you can run `skaffold dev`. It automatically tails the logs and delete the resources when interrupted (i.e. with a `Ctrl + C`).

### Tests

For running unit tests, use

```bash
make test
```

For running benchmark tests, use:

```bash
make benchmark-test
```

There are also some basic E2E tests, they are prepared to run using
[Minikube](https://github.com/kubernetes/minikube). To run them, execute:

``` bash
make e2e-test
```

You can specify against which version of K8s you want to execute the tests:

``` bash
E2E_KUBERNETES_VERSION=v1.10.0 E2E_START_MINIKUBE=yes make e2e-test
```

### Documentation

Please use the [Open Api 3.0 spec file](openapi.yaml) as documentation reference. Note that it describes the schema of the requests the webhook server replies to. This schema depends on the currently supported Kubernetes versions.

You can go to [editor.swagger.io](editor.swagger.io) and paste its contents there to see a rendered version.

### Performance

Please refer to [docs/performance.md](docs/performance.md).

## Contributing

We welcome code contributions (in the form of pull requests) from our user community. Before submitting a pull request please review [these guidelines](./CONTRIBUTING.md).

Following these helps us efficiently review and incorporate your contribution and avoid breaking your code with future changes.
