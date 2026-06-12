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
│ FastRG Controller      │──────────────────┘     │                └───────────▲────────────┘ 
│ REST :8443             │        ┌───────────────▼────────┐                   │ IPoE over VLAN
│ gRPC :50051            │consume │ Kafka                  │        ┌──────────▼─────────────┐
│ Prometheus: 55688      │◄───────│ FastRG node events     │        │ OLT                    │
└──────┬──────────▲──────┘        └────────────────────────┘        │ GPON aggregation       │
       │          │                                                 └─────▲────────────▲─────┘
       ▼          │                                                       │ PON        │ PON
┌────────────────────────┐                                                ▼            ▼
│ PostgreSQL             │                                          ┌──────────┐  ┌──────────┐
│ FastRG config history  │                                          │ ONT      │  │ ONT      │
│ FastRG node events     │                                          └────▲─────┘  └────▲─────┘
└────────────────────────┘                                               │ IPoE        │ IPoE
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
