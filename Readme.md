# FastRG(Fast Residential Gateway) Controller

[![FastRG Controller CI](https://github.com/FastResidentialGateway/fastrg-controller/actions/workflows/ci.yml/badge.svg)](https://github.com/FastResidentialGateway/fastrg-controller/actions/workflows/ci.yml)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD%203--Clause-blue.svg)](https://opensource.org/licenses/BSD-3-Clause)

This is an SDN-enabled and open source Residential Gateway Controller, designed to work together with the [Fast Residential Gateway Node](https://github.com/FastResidentialGateway/fastrg-node) dataplane deployed at the Central Office. Its purpose is to enable more efficient and centralized management of residential broadband networks ranging from 1 Gbps up to 25 Gbps, while achieving zero-touch deployment of new broadband subscribers.

## Features / Key Capabilities

- SDN-based architecture – Provides programmability and centralized control for residential broadband networks via REST API and gRPC.

- Seamless dataplane integration – Works in tandem with the Fast Residential Gateway Node deployed in the Central Office.

- High-speed broadband support – Scales from 1 Gbps to 25 Gbps for next-generation residential access.
    - Support PPPoE Client
    - Support DHCP server for per-subscriber LAN users
    - Support VLAN tagging for subscriber traffic
    - Support SNAT and port forwarding for subscriber traffic

- Zero-touch provisioning – Automates subscriber onboarding with no manual intervention required.

- Centralized management – Simplifies operations by consolidating control into a single controller plane.

- CAPEX and OPEX reduction – Service provider can only deploy a small cheap ONT device with bridge only functionality in subscriber's residence.

- Network security reduction – Centralized control reduces the attack surface and simplifies security management.

- Flexible configuration - Support dynamic subscriber and HSI(High Speed Internet) configuration changes via API without service disruption.

## Service Architecture

```
FastRG Components Overview

┌──────┐                                  CONFIG STORAGE           DATA PLANE
│FastRG│   ┌────────────┐if primary failed┌────────────┐           ┌────────────────────────┐
│ User │──▶│ FastRG CLI │ ─ ─ ─ ─ ─ ─ ─ ─▶│ etcd :2379 │           │ Backbone / Core Network│
└─┬────┘   └───────┬─┬──┘                 └─▲───────┬──┘           │ Internet uplink        │
  │                │ └ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─ ─│─ ─ ─ ─│─ ─ ─ ─ ┐     └────────────▲───────────┘
  │                │     if etcd unavailable│       │        │                  │ routed IP
  │                │                        │       │watch   │     ┌────────────▼───────────┐
  ▼                │                        │       │config  │     │ BNG / BRAS             │
┌─────────────────┐│                        │       │        │     │ PPPoE server           │
│FastRG Controller││                        │       │        │     └───────────▲────────────┘
│web frontend     ││                        │       │        │                 │ PPPoE / IPoE
│http(s):8080/8443││ primary path           │       │        │     ┌───────────▼────────────┐
└──────┬──────────┘│                        │       │        └ ─ ─▶│ FastRG Node            │
       │           │                        │       └──────────────┤ PPPoE client/NAT       │
       ▼           ▼                        │     ┌────────────────│ DHCP server            │
┌────────────────────────┐  write config    │     │ write events   │ gRPC :50052            │
│ FastRG Controller      │──────────────────┘     │                │ Prometheus: 55688      │
│ REST :8443             │        ┌───────────────▼────────┐       └───────────▲────────────┘
│ gRPC :50051            │consume │ Kafka                  │                   │ IPoE over VLAN
│ Prometheus: 55688      │◄───────│ FastRG node events     │        ┌──────────▼─────────────┐
└──────┬──────────▲──────┘        └────────────────────────┘        │ OLT                    │
       │          │                                                 │ GPON aggregation       │
       ▼          │                                                 └─────▲────────────▲─────┘
┌────────────────────────┐                                                │ PON        │ PON
│ PostgreSQL             │                                                ▼            ▼
│ FastRG config history  │                                          ┌──────────┐  ┌──────────┐
│ FastRG node events     │                                          │ ONT      │  │ ONT      │
└────────────────────────┘                                          └────▲─────┘  └────▲─────┘
                                                                         │ IPoE        │ IPoE
                                                                  ┌──────▼─────┐  ┌────▼───────┐
                                                                  │Subscriber 1│  │Subscriber 2│ ...
                                                                  │DHCP client │  │DHCP client │
                                                                  └────────────┘  └────────────┘
```

## Deployment
The FastRG Controller can be deployed using Kubernetes, or Helm charts. There are examples to deploy FastRG controller in Kubernetes and Helm. Please refer to the following documentation for detailed deployment instructions:
- [Kubernetes and Helm Chart Deployment Guide](deployment/README.md)

The FastRG system must work with an etcd cluster for configuration storage. You can either deploy your own etcd cluster or deploy an etcd service in Kubernetes cluster.
- The Etcd service must enable the `2379` port for FastRG controller and node to store and retrieve configuration data.

## Operation
- The FastRG Controller provides a web-based user interface for easy management and monitoring of residential broadband networks. Additionally, it offers REST API and gRPC interfaces for programmatic access and integration with other systems.
    - The web UI can be accessed at `http://<controller-ip>:8080` or `https://<controller-ip>:8443` by default.
    - The gRPC server listens on port `50051` by default.
    - The port `8444` with https can be used for accessing FastRG controller log file.
    - FastRG controller also provides Swagger API documentation for REST API at `http://<controller-ip>:8443/swagger/index.html` by default.
- It also provides Prometheus metrics endpoint for monitoring purposes. The Prometheus metrics can be accessed at `http://<controller-ip>:55688/metrics` by default.
    - All metrics name are prefixed with `fastrg_`, please use panels in Grafana dashboard to search them.
- Please make sure all above ports are enabled in the firewall settings to allow proper communication.

The controller uses a single-operator account model and does not provide public registration. Provision the first account at deployment time with `tools/create_user`. An authenticated user can create additional accounts through `POST /api/users`.

## Quick Start

### To build the binary, run:
```bash
make build
```
### To build Docker image, run:
```bash
make docker-build
```
### To pull Docker image from registry, run:
```bash
docker pull ghcr.io/fastresidentialgateway/fastrg-controller:latest
```
### To show all available make options, run:
```bash
make help
```
### To run example Kubernetes environment, run:
```bash
make k8s-create-test-env
make k8s-deploy
```
### To clean up test Kubernetes environment, run:
```bash
make k8s-destroy-test-env
```
### Register the FastRG Node with the Controller
Follow the instructions in the [FastRG Node repository](https://github.com/FastResidentialGateway/fastrg-node) to deploy the FastRG Node and register it with the FastRG Controller. Then you can manage the FastRG Node using the FastRG Controller's web UI or API.

## Testing

The test suite is split into layers according to its external-service requirements:

- **One-shot local suite:** `./tools/run_tests.sh` starts disposable etcd, PostgreSQL, and Kafka services, then runs the unit tests, integration tests, in-process Kafka/projection end-to-end tests, the 50-assertion REST smoke suite on dedicated local ports, the full-stack failure/recovery harness, and a final combined coverage summary. The complete run takes approximately 15 minutes. The script accepts no arguments, and the EXIT cleanup removes the throwaway services and restores any long-lived local test containers it paused.
- **Pure unit tests:** The Go package phase of `make test` requires no external services when `TEST_*` is unset; the gated integration tests skip automatically. Use `make test-go` to run only this Go phase without the REST smoke suite.
- **etcd, database, and leader integration tests:** Set `TEST_ETCD_ENDPOINTS` and/or `TEST_DATABASE_URL` to run the applicable integration paths against disposable services.
- **In-process service end-to-end tests:** The Kafka and projection suites exercise services in process and require the applicable combination of `TEST_ETCD_ENDPOINTS`, `TEST_DATABASE_URL`, and `TEST_KAFKA_BROKERS`; the full Kafka path uses all three.
- **REST smoke tests:** `tools/test_script.sh`, also invoked by `make test`, builds and launches the controller and exercises its REST API. Local `run_all_tests` mode defaults to HTTPS `18443`, HTTP redirect `18080`, gRPC `15051`, log HTTPS `18444`, and self-managed test-etcd `12380`. Target mode (for example, `tools/test_script.sh <IP> run_feature_tests`) retains HTTPS `8443`, HTTP redirect `8080`, gRPC `50051`, log HTTPS `8444`, and test-etcd `2379`. Explicit port environment variables always override these defaults, and local conflicts or invalid values fail loudly.
- **Full-stack failure and recovery tests:** The [`e2e_test/`](e2e_test/README.md) harness covers etcd, PostgreSQL, Kafka, controller, and node failure/recovery scenarios. See its README for setup and execution details.

### Coverage

<!-- coverage:begin -->
The following results were measured on 2026-07-19 with disposable etcd, PostgreSQL, and Kafka containers and all three `TEST_*` variables set:

| Package | Coverage |
|---|---:|
| `internal/utils` | 100.0% |
| `internal/validation` | 100.0% |
| `internal/storage` | 88.1% |
| `internal/db` | 80.3% |
| `internal/kafka` | 74.7% |
| `internal/projection` | 74.2% |
| `internal/leader` | 61.1% |
| `internal/server` | 50.4% |
| **Merged total** | **58.0%** |

Each percentage is the statement coverage of that package by the entire test suite, calculated from a single merged coverage profile.
<!-- coverage:end -->

Coverage changes as the codebase evolves. Run `make cover` to obtain current results instead of relying on this snapshot.

### Reproducing the coverage measurement

> **Warning:** Never point integration tests at etcd or PostgreSQL instances containing real data. These tests may clear keys and database tables. Use dedicated throwaway services only.

The one-shot helper supplies all required `TEST_*` variables and disposable services. It performs one instrumented `go test ./...` invocation for the unit, integration, and in-process end-to-end layers instead of rerunning them solely for coverage, then runs the REST smoke suite and the full-stack failure/recovery harness before printing the total coverage. A complete run takes approximately 15 minutes. It relies on the REST smoke script's dedicated local port defaults and does not require Python. Ensure Docker, Go, `sudo`, `swag`, `openssl`, `curl`, and `jq` are available, then run:

```bash
./tools/run_tests.sh
```

The full-stack harness under `e2e_test/` also remains available as a separate entry point.

## Road map
- Improve web UI for better user experience
- Support IPv6 dataplane configuration
- Support IGMP/IPTV traffic passthrough configuration for IPTV service
