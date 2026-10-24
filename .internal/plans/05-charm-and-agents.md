# Phase 05: Human CLI, Charm TUI and agent UX

**Depends on:** phase 00; integrate real actions after phases 03/04. **Goal:** same behavior whether the user is a human or an autonomous agent.

### Tasks

- [ ] **P05-01** Define UI-independent operation read models and an event subscription interface. Build synthetic fixtures for visual work before real deployments exist. Never fake a real deployment result.
- [ ] **P05-02** Create reusable Charm views using pinned compatible Bubble Tea, Lip Gloss and Bubbles versions: app list, service status, release history, deployment diff, progress/event timeline and logs. Design for small terminals and no-color mode.
- [ ] **P05-03** Add clear interactive flows for `init`, target selection, plan review and **operator-confirmed** apply where authorized. Use Huh only if it materially improves forms and dependency compatibility; do not add it by reflex.
- [ ] **P05-04** Implement machine JSON and optional JSONL with schema version, operation ID, stable error codes, unambiguous accepted/running/failed states, reconnectable event cursor and strict no-prompts policy.
- [ ] **P05-05** Make command status/logs stream bounded and cancellation-aware. Detaching from a view does not kill a target-side deployment. Offer explicit read-only observe mode; redact secrets in all presentations.
- [ ] **P05-06** Write example automation scripts that call Brine with noninteractive flags; avoid having agents scrape terminal rendering or infer success from a green icon.
- [ ] **P05-07** Snapshot and integration-test human/JSON parity for one successful and one failed deployment, including an interrupted client and resumed operation observation.

- [ ] **P05-08** Agent guide: a short document an agent such as ChatGPT can be given to manage a host end to end. It covers commands, the plan/apply flow, reading `kind` and exit codes, `diagnose`-first troubleshooting, and what is refused and why.

### Exit gate

An agent can plan, apply, poll and diagnose with structured responses; a human can perform the same actions through a readable TUI backed by identical domain functions. No TUI parser or second deploy engine exists.

**Evidence:** T01, T09, T20, T23. Stable JSON contracts should be versioned before documenting third-party integrations.
