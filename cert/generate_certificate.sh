#!/usr/bin/env sh

set -e

usage() {
  cat <<EOF
Generate certificate from approved CSR suitable for use with New Relic Mutating Webhook.
The server key/cert k8s CA cert are stored in a k8s secret.
usage: ${0} [OPTIONS] <key_file>

<key_file>: file path for the key to generate the certificate in PEM format.

Options are:
    --help               [Optional] Display this help message.
    --namespace <ns>     [Optional] Namespace for the webhook and secret. Def: default.
EOF
  exit 1
}

# default values
svc="newrelic-webhook-svc"
cfg="newrelic-webhook-cfg"
secret="newrelic-webhook-secret"

# arguments
ns="default"
key="$1"
if [ "$2" == "--namespace" ]; then
    ns="$3"
fi
csr=${svc}.${ns}

# input validation
[[ "$1" == "--help" ]] && usage
if [[ "${ns}" == "" ]]; then
    echo "ERROR: Namespace cannot be empty"
    exit 1
fi

if [[ ! -x "$(command -v openssl)" ]]; then
  echo "ERROR: openssl not found"
  exit 1
fi

# wait until CSR is available
echo "INFO: checking CSR..."
set +e
while true; do
  if kubectl get csr "${csr}"; then
      break
  fi
done
set -e

# verify certificate has been signed
cert=$(kubectl get csr "${csr}" -o jsonpath='{.status.certificate}')
if [[ "${cert}" == "" ]]; then
    echo "ERROR: CSR has not been approved, to continue: kubectl certificate approve \"${csr}\""
    exit 1
fi

if [[ "${cert}" = "" ]]; then
  echo "ERROR: Signed certificate did not appear on the CSR resource \"${csr}\""
  exit 1
fi

tmpdir=$(mktemp -d)
echo "INFO: creating certificate files in tmpdir ${tmpdir} "

echo "${cert}" | openssl base64 -d -A -out "${tmpdir}/server-cert.pem"

# create the secret with CA cert and server cert/key
kubectl create secret tls "${secret}" \
      --key="${key}" \
      --cert="${tmpdir}/server-cert.pem" \
      --dry-run -o yaml |
  kubectl -n "${ns}" apply -f -

caBundle=$(kubectl get configmap -n kube-system extension-apiserver-authentication -o=jsonpath='{.data.client-ca-file}' | base64 | tr -d '\n')

set +e
# patch the webhook adding the caBundle. It uses an `add` operation to avoid errors in OpenShift
while true; do
  echo "INFO: Trying to patch webhook adding the caBundle..."
  if kubectl patch mutatingwebhookconfiguration "${cfg}" --type='json' -p "[{'op': 'add', 'path': '/webhooks/0/clientConfig/caBundle', 'value':'${caBundle}'}]"; then
      break
  fi
  echo "INFO: webhook not patched. Retrying in 5s..."
  echo "INFO: did you already create the webhook? kubectl apply -f deploy/newrelic-webhook.yaml"
  sleep 5
done
