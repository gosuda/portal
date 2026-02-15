# Development Principles and Operational Guide

This document defines the core principles and operational scope of the Portal project.
It aims to prevent unnecessary feature expansion or direction drift, and to maintain a consistent user experience.

---

## 1. Usability Invariance Principle
Portal developers must ensure that any new feature does not alter existing usability.
A "change in usability" includes, but is not limited to:

- Modifications to the Portal usage flow
- Changes to the deployment or configuration process
- Alterations to the SDK development environment
- Increases in codebase complexity
- Any similar impacts that may affect the user experience

---

## 2. Prior Agreement for Changes
If a feature impacts usability, the proposer must provide a clear written rationale and obtain prior agreement from the team before proceeding.

- A merge is permitted only when at least one reviewer (other than the proposer) approves the change.

---


## 3. Testing and Quality Assurance
Unfinished or experimental features must not be merged directly into the main branch.
All new features must be fully tested and verified in a personal branch before merging.

---

## 4. Project Philosophy and Scope
Portal serves as a relay layer that allows individuals to publicly expose locally running services, with built-in end-to-end encryption.

- When proposing new features, include sufficient justification and follow the agreement process described above.
- Approved features must be documented and tracked in the project roadmap.

---

## 5. Principles for Resolving Disagreements
If differences of opinion arise, resolve them through constructive discussion and consensus.

---
