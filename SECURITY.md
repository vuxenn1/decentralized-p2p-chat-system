# Security Policy

## Status

This project is in stabilization. It is not production-ready and should not be used for sensitive communication.

## Reporting a vulnerability

Please do not open a public issue for security problems.

Use GitHub's private vulnerability reporting for this repository. If that is unavailable, contact the maintainer directly through the contact information on the repository profile.

Include:

- a short description
- affected component and version
- reproduction steps or a proof of concept
- impact assessment
- any suggested fix

Do not test against devices, networks or accounts you do not own or have permission to test.

## Scope

In scope:

- the Go core in this repository
- the public web demo
- the local API and its authentication
- protocol, storage and discovery code

Out of scope for this repository:

- the private Flutter application
- third-party services and bootstrap nodes
- dependencies without a demonstrated impact on this project

## Disclosure

The maintainer will acknowledge a report, investigate it and coordinate a fix and disclosure timeline. There is no bug bounty.

## Supported versions

There are no supported production versions yet. Security fixes target the current main branch.
