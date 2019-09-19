# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## 0.0.3

### Added

- Add environment variable parameter `NEW_RELIC_K8S_WEBHOOK_IGNORE_NAMESPACES` to ignore a list of namespaces. 
  Default ignored namespaces are `kube-system` and `kube-public`. It can be configured on the `newrelic-webhook.yaml`.

- Scripts for manual certificate generation and CSR approval.

- Resource requests for sidecar.

- Documentation on HPA tuning for sidecars.

- Support for Apache, Cassandra, Consul, Couchbase, Haproxy, jmx-tomcat, Kafka, Memcached, Nagios, Postgresql.

### Changed

- Container user is now 1000 instead of root.

- Volume mounts prefixed with `/nri-sidecar/` to avoid collisions.

- Secret management for NR license.

### Fixed

- Documentation on certificate.

- Issue when sidecar creation failed during mutation on slow systems, meanwhile configmap is not available.

## 0.0.2

- Initial version of the webhook.

- Support for Mysql, RabbitMQ, Nginx, MongoDB, Redis, ElasticSearch.