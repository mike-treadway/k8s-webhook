# Changelog
All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## 1.0.1
- Add environment variable parameter `NEW_RELIC_K8S_WEBHOOK_IGNORE_NAMESPACES` to ignore a list of namespaces. 
  Default ignored namespaces are `kube-system` and `kube-public`. It can be configured on the `newrelic-webhook.yaml`.

## 1.0.0
- Initial version of the webhook.
