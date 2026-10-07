# 0001. Record architecture decisions

Date: 2026-10-07 · Status: accepted

## Context

The plan (`docs/PLAN.md`) is the master copy of what Portenv is and how it is built. Decisions that change it need a record of why, so later work does not reopen them by accident.

## Decision

Every decision that changes the plan gets a short ADR in `docs/adr/`, numbered in order and using `0000-template.md`. The same change updates `docs/PLAN.md`. Accepted ADRs are not edited; a later ADR supersedes them.

## Consequences

The plan stays readable as the current state; the ADRs carry the history and reasoning.
