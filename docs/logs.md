# Accessing the On-host integrations logs

The logs of both the Agent running in sidecar mode and the monitored On-Host integrations are visible in the
`newrelic-sidecar` containers that are attached to the services.

If you want to know which pods are running with a sidecar, you can run the following JSON query with `kubectl`:

```
$ kubectl get pods --all-namespaces \
  -o=jsonpath='{range .items[*]}{"\n"}{.metadata.name}{":\t"}{range .spec.containers[*]}{.name}{", "}{end}{end}' |
  grep newrelic-sidecar
```

And you will see a list of all the pods with a sidecar container. For example:

```
mongos1-59989958-44rtw:	mongos1, newrelic-sidecar,
mongos2-cb96676c-f6qt6:	mongos2, newrelic-sidecar,
mongosh1-1-99b79c4f5-8t5p8:	mongosh1-1, newrelic-sidecar,
mongosh2-1-5c6f44b7b7-gjdtp:	mongosh2-1, newrelic-sidecar,
mongosh3-1-687488b796-dcw5c:	mongosh3-1, newrelic-sidecar,
```

Then you can run `kubectl logs` to individually access the logs of each sidecar, e.g.:

```
$ kubectl logs -f mongosh2-1-5c6f44b7b7-gjdtp newrelic-sidecar
```

If you want to access the logs from all the sidecars at the same time, your deployment file
should add a unique label to all the pods running a sidecar, for example, `log: newrelic-sidecar`:

```yaml
(...)
apiVersion: apps/v1beta1
kind: Deployment
metadata:
  name: mongosh1-1
spec:
  replicas: 1
  template:
    metadata:
      annotations:
        newrelic.com/integrations-sidecar-configmap: "mongodb-newrelic-integrations-config"
        newrelic.com/integrations-sidecar-imagename: "newrelic/k8s-nri-mongodb"
      labels:
        log: newrelic-sidecar
        name: mongosh1-1
        run: mongosh1-1
(...)
```

Then you can query all the `newrelic-sidecar` containers whose pods are labeled with `log=newrelic-sidecar`:

```
$ kubectl logs -f -l log=newrelic-sidecar -c newrelic-sidecar
```
