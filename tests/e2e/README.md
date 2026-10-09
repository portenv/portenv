# End-to-end tests

Two-machine scenarios and crash tests against real restic repositories. Built from milestone 0.3.

`portenvd` and the runner reach boxes only through the agent channel. A script's own `docker exec` only inspects a box or simulates a failure, and `daemon.sh` and `two-machines.sh` count every exec event to prove it.

| Script | What it checks |
| --- | --- |
| `daemon.sh` | `portenvd` as the app uses it: open, terminal, save points, revert, close, restore, quitting, the agent's failures, waking, take-over after `kill -9` and an update relaunch (ADR 0014) |
| `two-machines.sh` | Two machines, each with its own `portenvd`, one storage: the lease, A → B → A with checksums, take-over and resume rule 4 |
| `sftp-storage.sh` | SFTP storage through the box's own route |
| `restic-isolation.sh` | The restic password stays out of reach of everything in the box but restic (ADR 0005) |
| `server-session.sh` | A real server through the runner: Move To and back, the terminal there, an impostor on the agent's port, a runner restart |

`server-gate.sh`, the Phase 0 gate, was retired in 1.1 together with the CLI commands it drove (`resume`, `save`, `close`, `move`), which reached boxes with `docker exec`. It last ran when Phase 0 closed (docs/milestones/0.5.md). `server-session.sh` is the server test now.
