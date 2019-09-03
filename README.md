# Kubernetes webhook for New Relic

## Definition

The webhook intercepts *POD creation* requests to the Kubernetes API and mutates them in the following ways:

* Enables the **monitoring of services running in pods**, by injecting a *sidecar* containing the agent and relevant integration into all labelled pods.

* Injects APM agents **required metadata** to identify Kubernetes objects. The following *environment variables* are injected:

    - `NEW_RELIC_METADATA_KUBERNETES_CLUSTER_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_NODE_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_NAMESPACE_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_DEPLOYMENT_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_POD_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_CONTAINER_NAME`
    - `NEW_RELIC_METADATA_KUBERNETES_CONTAINER_IMAGE_NAME`

    These environment variables can either be automatically injected using a `MutatingAdmissionWebhook`, or be set manually by the customer. 

    New Relic provides an easy method for deploying this automatic approach.
    
The `newrelic-webhook-svc` service internally exposes two ports:

* `8443`, required by the service. It can be configured in the `newrelic-webhook.yml` deployment file:
   https://github.com/newrelic/k8s-webhook/blob/master/deploy/newrelic-webhook.yaml#L55
* `8080`, required for health check of the service.

## Setup

### 1) Requirements

Check if MutatingAdmissionWebhook is enabled on your cluster. This feature requires *Kubernetes 1.9* or later. Verify that the kube-apiserver process has the admission-control flag set.

```
$ kubectl api-versions | grep admissionregistration.k8s.io/v1beta1
admissionregistration.k8s.io/v1beta1
```


### 2) Install certificate

This webhook needs to be authenticated by the Kubernetes extension API server, so it will need to have a signed certificate from a CA trusted by the extension API server. The certificate management is isolated from the webhook server and a secret is used to mount them. 


#### Automatic installation

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

This job will execute the shell script [k8s-webhook-cert-manager/generate_certificate.sh](https://github.com/newrelic/k8s-webhook-cert-manager/blob/master/generate_certificate.sh) to setup everything. This script will:

1. Generate a server key.
2. If there is any previous CSR (certificate signing request) for this key, it is deleted.
3. Generate a CSR for such key.
4. The signature of the key is then approved.
5. The server's certificate is fetched from the CSR and then encoded.
6. A secret of type `tls` is created with the server certificate and key.
7. The k8s extension api server's CA bundle is fetched.
8. The mutating webhook configuration for the webhook server is patched with the k8s api server's CA bundle from the previous step. This CA bundle will be used by the k8s extension api server when calling our webhook.

If you wish to learn more about TLS certificates management inside Kubernetes, check out [the official documentation for Managing TLS Certificate in a Cluster](https://kubernetes.io/docs/tasks/tls/managing-tls-in-a-cluster/#create-a-certificate-signing-request-object-to-send-to-the-kubernetes-api).

#### Manual installation

Otherwise, if you are managing the certificate manually you will have to create the TLS secret with the signed certificate/key pair and patch the webhook's CA bundle.

This option will be relevant in case you don't want to grant CSR approval permissions to the webhook service-account generated on the automatic job mentioned above.

We provide a couple scripts for this purpose.

```
cert
├── generate_certificate.sh
└── generate_csr.sh
```

> `openssl` is required for these scripts

##### generate_csr.sh
```
Generate certificate signing request suitable for use with New Relic Mutating Webhook.
The server key/cert k8s CA cert are stored in a k8s secret.
usage: ${0} [OPTIONS]

Supported options are:
    --help               [Optional] Display this help message.
    --key <path>         [Optional] Path for a key to the create the certificate in PEM format. Def: autogenerated.
    --namespace <ns>     [Optional] Namespace for the webhook and secret. Def: default.
```

##### cert/generate_certificate.sh

````
The server key/cert k8s CA cert are stored in a k8s secret.
usage: ${0} [OPTIONS] <key_file>

<key_file>: file path for the key to generate the certificate in PEM format.

Options are:
    --help               [Optional] Display this help message.
    --namespace <ns>     [Optional] Namespace for the webhook and secret. Def: default.
````


##### Steps

1. Run `cert/generate_csr.sh` to **generate a CSR** from a provided (or automatically generated) key
2. **Approve** the CSR following the output of the previous script.
   * This might look like: `kubectl certificate approve "newrelic-webhook-svc.default"`
3. Run `cert/generate_certificate.sh` to generate and **install the certificate** for the approved CSR.

##### Example

1. Generate CSR:

```
$ cert/generate_csr.sh

INFO: creating certificate files in tmpdir /tmp/foo/
Generating RSA private key, 2048 bit long modulus
  ...................................................+++
  ...................................................+++
  e is 65537 (0x10001)
certificatesigningrequest.certificates.k8s.io/newrelic-webhook-svc.default created

K8s CSR:  newrelic-webhook-svc.default
Key file: /tmp/foo/server-key.pem

Remaining steps:
Approve CSR:                    kubectl certificate approve "newrelic-webhook-svc.default"
Sign and install certiticate:   cert/generate_certificate.sh /tmp/foo/server-key.pem
```

2. Approve CSR

```
$ kubectl certificate approve "newrelic-webhook-svc.default"
```


3. Generate and install certificate

```
$ cert/generate_certificate.sh /tmp/foo/server-key.pem

INFO: checking CSR...
  NAME                           AGE   REQUESTOR       CONDITION
  newrelic-webhook-svc.default   11m   minikube-user   Approved,Issued
INFO: creating certificate files in tmpdir /var/bar/
secret/newrelic-webhook-secret created
INFO: Trying to patch webhook adding the caBundle...
mutatingwebhookconfiguration.admissionregistration.k8s.io/newrelic-webhook-cfg patched
```

Either certificate management choice made, the important thing is to have the secret created with the correct name and namespace. As long as this is done the webhook server will be able to pick it up.



### 3) Install the injection

If you choose to set the license key environment variable from a secret, execute the following command:
```bash
kubectl create secret generic newrelic-agent-secret --from-literal=nria-license-key='<NRIA_LICENSE_KEY>'
```

Otherwise, you can open `deploy/newrelic-webhook.yaml` and edit `NRIA_LICENSE_KEY` environment variable. Uncomment `#value: "<NRIA_LICENSE_KEY>"`, set your license key and comment/remove the `valueFrom` block.

Edit `deploy/newrelic-webhook.yaml` to configure the variable `clusterName`

Then execute the following command:
```bash
$ kubectl apply -f deploy/newrelic-webhook.yaml
```

Executing this:

- creates `newrelic-webhook-deployment` and `newrelic-webhook-svc`.
- registers the `newrelic-webhook-svc` service as a MutatingAdmissionWebhook with the Kubernetes API.

### 4) Enable the webhook on your namespaces

The webhook will only monitor namespaces that have the `newrelic-webhook` label set to `enabled`.

```
$ kubectl label namespace <namespace> newrelic-webhook=enabled
```

### 5) Enable monitoring of pods

A sidecar will be injected into all pods having the `newrelic.com/integrations-sidecar-configmap` annotation set to the name of a config map object, in the same namespace as the targeted pod, containing the integration config. 
The `newrelic.com/integrations-sidecar-imagename` annotation is used to specify the sidecar image to be injected.

The injector expects the config map to have a `config.yaml` file and an optional `definition.yaml` file, which are the usual configurations for integrations.
The two will be mounted to `/nri-sidecar/newrelic-infra/integrations.d/integration.yaml` and `/nri-sidecar/newrelic-infra/newrelic-integrations/definition.yaml` respectively
and overwrite any of these if already present in the sidecar image.

Passwords and other secret information passed as arguments to the integrations can be suplied as an environment variable backed by a kubernetes secret. If the name
of an integration argument starts with `$`, the injector assumes this refers to an environment variable that is defined in the targeted pod, with the same name (minus the `$` symbol).

The agent license and other agent configuration environment variables can be added to the injector deployment and they will be all copied to the injected sidecars.

#### Example configuration:

```
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
                - /nri-sidecar/newrelic-infra/newrelic-integrations/bin/nr-mysql
            prefix: config/mysql
            interval: 30
---
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
        newrelic.com/integrations-sidecar-imagename: "newrelic/k8s-nri-mysql"
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
```

## Certificate rotation

The webhook server has a file watcher pointed at the secret's folder that will trigger a certificate reload whenever anything is created or modified inside the secret. This allows easy certificate rotation with an update of the TLS secret that is created by running:

```bash
$ namespace=default # Change the namespace here if you also changed it in the yaml files.
$ serverCert=$(kubectl get csr newrelic-webhook-svc.${namespace} -o jsonpath='{.status.certificate}')
$ tmpdir=$(mktemp -d)
$ echo ${serverCert} | openssl base64 -d -A -out ${tmpdir}/server-cert.pem
$ kubectl patch secret newrelic-webhook-secret --type='json' \
    -p "[{'op': 'replace', 'path':'/data/tls.crt', 'value':'$(serverCert)'}]"
$ rm -rf $(tmpdir)
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

#### Configuring Horizontal Pod Autoscaler

If you have defined a HPA for a pod that will be monitored with New Relic Infrastructure Agent, you will have to take into account the New Relic sidecar resource request/limit when defining the auto scaling threshold. This is because the resource request/limit are set on the container level while the auto scaling threshold is set on pod. For more information read the [official documentation](https://kubernetes.io/docs/concepts/configuration/manage-compute-resources-container/#resource-requests-and-limits-of-pod-and-container) for Resource requests and limits of Pod and Container.

The New Relic sidecar will have defined by default values for CPU and memory resource request/limit:
 * `cpu: "100m"`
 * `memory: "64Mi"`

We suggest to take those values into account when defining the auto scaling target threshold.

e.g.

*  You have defined a container CPU request of `1000m` and a pod `targetCPUUtilizationPercentage` of `90%`
we suggest that you adjust the `targetCPUUtilizationPercentage` to: Floor(90 * 1000 / 1100)) = `81%`

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
