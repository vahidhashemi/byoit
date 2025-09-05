 
 
# Byoit

Byoit is a command-line tool written in Go for automating the installation and configuration of key IT service desk applications on Kubernetes clusters. Byoit streamlines the deployment of services such as **OpenLDAP**, **GitLab**, and **Nexus**, making it easy to bootstrap and manage your IT infrastructure.

## Features

- Installs and configures OpenLDAP, GitLab, and Nexus directly on Kubernetes
- Handles related services such as phpLDAPadmin and ltb-passwd for OpenLDAP
- Supports air-gapped (offline) environments by managing image pull policies and local Helm index downloads
- Debug output for troubleshooting installation steps
- Designed for quick, repeatable, and reliable IT service desk environment setup
- **No need for Helm CLI:** Byoit uses the Helm SDK internally, so you do not need to have the Helm binary installed

## Prerequisites

- Go 1.18+ installed
- Access to a Kubernetes cluster (via kubeconfig)
- `kubectl` installed locally
- (Optional) `curl` for local index file downloads

## Building

Clone the repository and build the binary:



