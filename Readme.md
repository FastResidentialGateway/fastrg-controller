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
┌──────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
 │ Management Plane                                                                                             │
 │ ┌──────────────────────────────────────────────────────────────────────────────────────────────────────────┐ │
 │ │  CLI  (fastrg-cli)                                                                                       │ │
 │ │                                                                                                          │ │
 │ │  write / PPPoE connect・disconnect                                                                        │ │
 │ │    ① primary    ──► Controller gRPC ──► [validate + CAS] ──► etcd                                       │ │
 │ │    ② fallback1  ──► etcd direct  (controller unreachable; minimal local validation)                      │ │
 │ │    ③ standalone ──► Node gRPC ──► offline queue (disk, timestamped) ──► flush to etcd on reconnect      │ │
 │ │                                                                          (CAS + timestamp merge)          │ │
 │ │  query                                                                                                    │ │
 │ │    show desire-config  → ① Controller  → ② etcd  → ③ Node gRPC + pending queue                         │ │
 │ │    show current-config → Node gRPC direct  (live running config + PPPoE / DHCP actual status)           │ │
 │ │    show config diff    → desire vs current; list drifted fields or "in-sync"                            │ │
 │ └─────────┬────────────────────────────────────────────────────────────┬────┬──────────────────────────────┘ │
 │           │ ③ to Node                                                  │ ②  │ ① to Controller               │
 └───────────┼────────────────────────────────────────────────────────────┼────┼────────────────────────────────┘
             │                                                            │    │
 ┌───────────┼────────────────────────────────────────────────────────────┼────┼────────────────────────────────┐
 │           ▼                                          Central Office    │    ▼                                │
 │  ┌──────────────────┐                                                  │  ┌─────────────────────────────────┐ │
 │  │    BNG/BRAS      │                                                  │  │      FastRG Controller          │ │
 │  │  PPPoE Server    │                                                  │  │      gRPC: 50051                │ │
 │  └──────────────────┘                                                  │  │      HTTP(s): 8080/8443         │ │
 │           ▲                                                            │  │      REST API: 8443             │ │
 │           │  PPPoE/IGMP/IPTV over VLAN                                 │  │      Prometheus: 55688          │ │
 │           ▼                                                            │  └──────────────┬──────────────────┘ │
 │  ┌──────────────────────┐                 ┌──────────────────────┐    │   CAS write │   │ watch configs/     │
 │  │     FastRG Node      │                 │     FastRG etcd      │◄───┘             │   │ → upsert/append DB │
 │  │     gRPC: 50052      │◄── watch ───────│     etcd: 2379       │◄─────────────────┘   │                    │
 │  │     PPPoE Client/NAT │   configs/      │  configs/{uuid}/     │──────────────────────┘                    │
 │  │     DHCP Server      │   desire_status │   hsi/{user_id}      │                                           │
 │  │                      │   → drives PPPoE│  desire_status        │  ┌──────────────────────────────────┐    │
 │  │  ┌────────────────┐  │     connect /   │  ∈ {connect,         │  │         PostgreSQL                │    │
 │  │  │ offline queue  │  │     disconnect  │    disconnect}        │  │  hsi_config_current  (upsert)     │    │
 │  │  │ (disk, ts'd)   │──┼─── flush ──────►│                      │  │  hsi_config_history  (append,     │    │
 │  │  └────────────────┘  │  on reconnect   │  [CAS: Txn(ModRev)]  │  │    mod_revision)                  │    │
 │  └──────────────────────┘  (CAS + ts mrg) │  no commands/        │  │  pppoe_status  (Kafka fed)        │    │
 │           │                               │  no failed_events/   │  │  node_events   (Kafka fed,        │    │
 │  Kafka    │  PPPoE: connecting/           │  no enable_status    │  │    replaces etcd failed_events/)  │    │
 │ producer  │  connected/                   └──────────────────────┘  │  etcd_watch_progress              │    │
 │           │  disconnecting/                                          │    (revision checkpoint)          │    │
 │           │  disconnected                                            └──────────────────────────────────┘    │
 │           │  config apply: ok / fail                                              ▲                          │
 │           │  runtime errors                                                       │ Kafka consumer            │
 │           ▼                                                                       │ (at-least-once,          │
 │  ┌────────────────────────────────────────────────────────────────────────────────┴──────────────────────┐  │
 │  │  Kafka  (topic: fastrg.node.events, partitioned by node_uuid)                                        │  │
 │  │  Node ──producer──► Broker ──consumer──► Controller  (idempotent write to PostgreSQL)                │  │
 │  └───────────────────────────────────────────────────────────────────────────────────────────────────────┘  │
 │                                                                                                              │
 │  ┌───────────────────┐                                                                                       │
 │  │       OLT         │◄─── IPoE over VLAN  (VLAN-A → Sub 1,  VLAN-B → Sub 2, ...)                          │
 │  └───────────────────┘                                                                                       │
 └──────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
          │  PON Network                    │  PON Network
          ▼                                 ▼
 ┌────────────────────┐          ┌────────────────────┐
 │ ┌────────────────┐ │          │ ┌────────────────┐ │
 │ │      ONT       │ │          │ │      ONT       │ │
 │ └────────────────┘ │          │ └────────────────┘ │
 │         ▲          │          │         ▲          │
 │         │  IPoE    │          │         │  IPoE    │
 │         ▼          │          │         ▼          │
 │ ┌────────────────┐ │          │ ┌────────────────┐ │
 │ │Subscriber Dev. │ │          │ │Subscriber Dev. │ │
 │ │  (DHCP client) │ │          │ │  (DHCP client) │ │
 │ └────────────────┘ │          │ └────────────────┘ │
 │    Subscriber 1    │          │    Subscriber 2    │
 └────────────────────┘          └────────────────────┘   ...
```

## Deployment
The FastRG Controller can be deployed using Kubernetes, or Helm charts. There are examples to deploy FastRG controller in Kubernetes and Helm. Please refer to the following documentation for detailed deployment instructions:
- [Kubernetes Deployment Guide](deployment/k8s/README.md)
- [Helm Chart Deployment Guide](deployment/README.md)

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

## Quick Start and test the FastRG Controller
### To build the binary, run:
```bash
make build
```
### To test the code, run:
```bash
make test
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

## Road map
- Improve web UI for better user experience
- Support IPv6 dataplane configuration
- Support IGMP/IPTV traffic passthrough configuration for IPTV service
